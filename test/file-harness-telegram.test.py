import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('telegram', Path(__file__).resolve().parents[1] / 'examples/file-harness/telegram.py')
telegram = importlib.util.module_from_spec(spec)
spec.loader.exec_module(telegram)


class TelegramTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        (self.home / 'telegram').mkdir()
        (self.home / 'inbox').mkdir()
        (self.home / 'telegram/config.json').write_text(json.dumps({'user_id': 10, 'chat_id': 20}))
        self.calls = []
        self.updates = []
        def api(method, parameters):
            self.calls.append((method, parameters))
            if method == 'getUpdates':
                return self.updates
            return {'message_id': 700}
        self.channel = telegram.Channel(self.home, api)
        self.addCleanup(self.channel.close)
        with self.channel.db:
            self.channel.set('imported', True)

    def update(self, uid=50, sender=10, chat=20, **extra):
        return {'update_id': uid, 'message': {'message_id': uid + 100, 'date': 123,
                'chat': {'id': chat, 'type': 'private'}, 'from': {'id': sender}, 'text': 'Human text', **extra}}

    def test_only_fixed_owner_and_private_chat_wake(self):
        self.updates = [self.update(50, sender=11), self.update(51, chat=21)]
        group = self.update(52); group['message']['chat']['type'] = 'group'
        bot = self.update(53); bot['message']['from']['is_bot'] = True
        self.updates += [group, bot, self.update(54)]
        self.channel.poll(0)
        self.assertEqual(self.channel.get('offset'), 55)
        files = list((self.home / 'inbox').iterdir())
        self.assertEqual(len(files), 1)
        msg = json.loads(files[0].read_text())
        self.assertEqual(msg['sender_id'], 10)
        self.assertEqual(msg['text'], 'Human text')
        self.assertEqual(msg['update_id'], 54)
        self.channel.poll(0)
        self.assertEqual(self.calls[-1][1]['offset'], 55)
        self.assertEqual(self.channel.db.execute('SELECT count(*) FROM inbound').fetchone()[0], 1)

    def test_persisted_inbound_survives_publish_failure_and_restart(self):
        self.updates = [self.update()]
        with patch.object(telegram, 'atomic', side_effect=OSError('disk')):
            with self.assertRaises(OSError):
                self.channel.poll(0)
        self.assertEqual(self.channel.get('offset'), 51)
        self.assertEqual(self.channel.status()['unpublished'], 1)
        restarted = telegram.Channel(self.home, self.channel.api)
        self.addCleanup(restarted.close)
        restarted.flush()
        self.assertEqual(restarted.status()['unpublished'], 0)
        self.assertTrue((self.home / 'inbox/telegram-50.json').is_file())

    def test_edited_and_forwarded_content_preserves_provenance(self):
        update = self.update(forward_origin={'type': 'hidden_user'}, document={'file_id': 'a'})
        update['edited_message'] = update.pop('message')
        self.channel.accept(update)
        self.channel.flush()
        item = json.loads((self.home / 'inbox/telegram-50.json').read_text())
        self.assertTrue(item['edited'])
        self.assertTrue(item['forwarded'])
        self.assertEqual(item['attachments']['document']['file_id'], 'a')

    def test_send_is_idempotent_and_never_takes_destination_from_message(self):
        first = self.channel.send('reply-1', 'hello', 150)
        self.assertEqual(self.channel.send('reply-1', 'hello', 150), first)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.calls[0][1]['chat_id'], 20)
        with self.assertRaises(ValueError):
            self.channel.send('reply-1', 'changed', 150)
        with self.assertRaises(ValueError):
            self.channel.send('large', '😀' * 2049)

    def test_uncertain_send_is_durable_and_never_automatically_retried(self):
        def fail(*_):
            raise telegram.APIError('unknown')
        self.channel.api = fail
        with self.assertRaises(telegram.APIError):
            self.channel.send('uncertain', 'hello')
        self.assertEqual(self.channel.status()['uncertain_sends'], [{'id': 'uncertain', 'status': 'unknown'}])
        self.channel.api = lambda *_: self.fail('Must not retry')
        with self.assertRaises(ValueError):
            self.channel.send('uncertain', 'hello')

    def test_known_rejection_is_recorded_and_requires_explicit_reconciliation(self):
        def reject(*_):
            raise telegram.APIError('rejected', rejected=True)
        self.channel.api = reject
        with self.assertRaises(telegram.APIError):
            self.channel.send('bad', 'hello')
        self.assertEqual(self.channel.db.execute("SELECT status FROM outbound WHERE id='bad'").fetchone()[0], 'rejected')

    def test_health_wakes_once_per_failure_episode(self):
        self.assertFalse(self.channel.health()['healthy'])
        p = self.home / 'inbox/telegram-health.json'
        p.unlink()
        self.channel.health()
        self.assertFalse(p.exists())
        self.channel.poll(0)
        self.assertTrue(self.channel.health()['healthy'])
        with self.channel.db:
            self.channel.set('watch', {})
        self.channel.health()
        self.assertTrue(p.exists())

    def test_import_preserves_offset_and_receipts_without_replaying_history(self):
        with self.channel.db:
            self.channel.set('imported', False)
        old = self.home / 'legacy.sqlite'
        db = sqlite3.connect(old)
        db.executescript('CREATE TABLE settings(key TEXT,value TEXT); CREATE TABLE deliveries(id TEXT,text TEXT,reply_to INTEGER,status TEXT,message_id INTEGER,error TEXT);')
        db.executemany('INSERT INTO settings VALUES(?,?)', [('binding', json.dumps({'chatId': 20, 'userId': 10})), ('offset', '900')])
        db.execute("INSERT INTO deliveries VALUES('old-reply','old text',1,'sent',2,NULL)")
        db.commit(); db.close()
        self.channel.import_legacy(old)
        self.assertEqual(self.channel.get('offset'), 900)
        self.assertEqual(self.channel.send('old-reply', 'old text', 1)['message_id'], 2)
        self.assertEqual(self.calls, [])
        self.assertEqual(list((self.home / 'inbox').iterdir()), [])
        with self.assertRaises(ValueError):
            self.channel.import_legacy(old)

    def test_unconfigured_channel_cannot_consume_updates(self):
        with self.channel.db:
            self.channel.set('imported', False)
        with self.assertRaises(ValueError):
            self.channel.poll(0)
        self.assertEqual(self.calls, [])


if __name__ == '__main__':
    unittest.main()
