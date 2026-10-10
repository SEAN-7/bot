#!/usr/bin/env python3
"""Agent-owned Telegram listener/send script; Python standard library only."""
import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import signal
import sqlite3
import sys
import time
import urllib.error
import urllib.request
import uuid


def atomic(path, value):
    path = Path(path)
    temporary = path.with_name('.' + path.name + '.' + uuid.uuid4().hex + '.tmp')
    with temporary.open('x') as stream:
        os.chmod(temporary, 0o600)
        stream.write(json.dumps(value, ensure_ascii=False) + '\n')
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)
    fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


class APIError(RuntimeError):
    def __init__(self, message, rejected=False):
        super().__init__(message)
        self.rejected = rejected


class API:
    def __init__(self, token):
        self.token = token

    def __call__(self, method, parameters):
        request = urllib.request.Request(
            'https://api.telegram.org/bot' + self.token + '/' + method,
            data=json.dumps(parameters).encode(), headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, timeout=65) as response:
                result = json.load(response)
        except urllib.error.HTTPError as error:
            # Never print a URL: it contains the channel credential.
            try:
                result = json.load(error)
            except Exception:
                raise APIError('Telegram response unavailable; outcome unknown') from None
        except Exception:
            raise APIError('Telegram response unavailable; outcome unknown') from None
        if not result.get('ok'):
            raise APIError('Telegram rejected request: ' + str(result.get('error_code')), rejected=True)
        return result['result']


class Channel:
    def __init__(self, home, api=None):
        self.home = Path(home)
        self.folder = self.home / 'telegram'
        self.config = json.loads((self.folder / 'config.json').read_text())
        for key in ('chat_id', 'user_id'):
            if type(self.config.get(key)) is not int or self.config[key] <= 0:
                raise ValueError('A fixed private owner chat and user ID are required')
        self.api = api or API(json.loads((self.folder / 'credentials.json').read_text())['token'])
        self.db = sqlite3.connect(self.folder / 'channel.sqlite', timeout=15)
        self.db.row_factory = sqlite3.Row
        self.db.executescript('''PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
          CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
          CREATE TABLE IF NOT EXISTS inbound(update_id INTEGER PRIMARY KEY,message_id INTEGER,
            envelope TEXT NOT NULL,published INTEGER NOT NULL DEFAULT 0);
          CREATE TABLE IF NOT EXISTS outbound(id TEXT PRIMARY KEY,text TEXT NOT NULL,
            reply_to INTEGER,status TEXT NOT NULL,message_id INTEGER,error TEXT);
        ''')
        os.chmod(self.folder / 'channel.sqlite', 0o600)

    def get(self, key, default=None):
        row = self.db.execute('SELECT value FROM settings WHERE key=?', (key,)).fetchone()
        return json.loads(row[0]) if row else default

    def set(self, key, value):
        self.db.execute('INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value',
                        (key, json.dumps(value)))

    def import_legacy(self, path):
        """Run once after stopping the old poller; never replays old messages."""
        if self.get('imported'):
            raise ValueError('Channel already imported')
        old = sqlite3.connect(Path(path).resolve().as_uri() + '?mode=ro', uri=True)
        try:
            settings = {k: json.loads(v) for k, v in old.execute('SELECT key,value FROM settings')}
            binding = settings['binding']
            if binding != {'chatId': self.config['chat_id'], 'userId': self.config['user_id']}:
                raise ValueError('Legacy binding does not match configured owner')
            if old.execute("SELECT 1 FROM deliveries WHERE status IN ('started','unknown') LIMIT 1").fetchone():
                raise ValueError('Reconcile uncertain legacy sends before handover')
            with self.db:
                self.set('offset', settings.get('offset', 0))
                self.set('imported', {'path': str(path), 'time': time.time()})
                self.db.executemany('INSERT INTO outbound VALUES(?,?,?,?,?,?)',
                                    old.execute('SELECT id,text,reply_to,status,message_id,error FROM deliveries'))
        finally:
            old.close()

    def accept(self, update):
        uid = update['update_id']
        if type(uid) is not int:
            raise ValueError('Invalid update ID')
        if uid < self.get('offset', 0):
            return
        msg = update.get('message') or update.get('edited_message') or {}
        owner = (msg.get('chat', {}).get('type') == 'private'
                 and msg['chat'].get('id') == self.config['chat_id']
                 and msg.get('from', {}).get('id') == self.config['user_id']
                 and not msg['from'].get('is_bot', False))
        with self.db:
            if owner:
                envelope = {'channel': 'telegram', 'kind': 'verified-owner-message',
                            'update_id': uid, 'message_id': msg['message_id'],
                            'chat_id': self.config['chat_id'], 'sender_id': self.config['user_id'],
                            'date': msg.get('date'), 'edited': 'edited_message' in update,
                            'text': msg.get('text', msg.get('caption', '')),
                            'forwarded': bool(msg.get('forward_origin')),
                            'attachments': {k: msg[k] for k in ('photo', 'document', 'audio', 'voice', 'video', 'sticker') if k in msg}}
                self.db.execute('INSERT OR IGNORE INTO inbound(update_id,message_id,envelope) VALUES(?,?,?)',
                                (uid, msg['message_id'], json.dumps(envelope, ensure_ascii=False)))
            # Advance only after a durable message record, including rejected senders.
            self.set('offset', uid + 1)

    def flush(self):
        for row in self.db.execute('SELECT update_id,envelope FROM inbound WHERE published=0 ORDER BY update_id').fetchall():
            atomic(self.home / 'inbox' / ('telegram-' + str(row['update_id']) + '.json'), json.loads(row['envelope']))
            # A crash between these stores can repeat an envelope, never lose it.
            with self.db:
                self.db.execute('UPDATE inbound SET published=1 WHERE update_id=?', (row['update_id'],))

    def poll(self, timeout=50):
        if not self.get('imported'):
            raise ValueError('Import the final legacy channel state before polling')
        self.flush()
        updates = self.api('getUpdates', {'offset': self.get('offset', 0), 'limit': 100,
                                         'timeout': timeout, 'allowed_updates': ['message', 'edited_message']})
        for update in sorted(updates, key=lambda item: item['update_id']):
            self.accept(update)
        self.flush()
        with self.db:
            self.set('watch', {'state': 'connected', 'checked_at': time.time(), 'error': None})

    def send(self, action_id, text, reply_to=None):
        if not action_id or len(action_id) > 200 or not text.strip() or len(text.encode('utf-16-le')) // 2 > 4096:
            raise ValueError('Use a stable action ID and 1..4096 UTF-16 units of text per send')
        # Reserve once under a DB lock. A second process cannot duplicate the send.
        self.db.execute('BEGIN IMMEDIATE')
        prior = self.db.execute('SELECT * FROM outbound WHERE id=?', (action_id,)).fetchone()
        if prior:
            self.db.rollback()
            if prior['text'] != text or prior['reply_to'] != reply_to:
                raise ValueError('Action ID already used for different content')
            if prior['status'] != 'sent':
                raise ValueError('Prior send is ' + prior['status'] + '; inspect effects before any retry')
            return dict(prior)
        self.db.execute("INSERT INTO outbound(id,text,reply_to,status) VALUES(?,?,?,'started')", (action_id, text, reply_to))
        self.db.commit()
        parameters = {'chat_id': self.config['chat_id'], 'text': text}
        if reply_to is not None:
            parameters['reply_parameters'] = {'message_id': reply_to, 'allow_sending_without_reply': True}
        try:
            message = self.api('sendMessage', parameters)
            with self.db:
                self.db.execute("UPDATE outbound SET status='sent',message_id=? WHERE id=?", (message['message_id'], action_id))
        except Exception as error:
            with self.db:
                self.db.execute('UPDATE outbound SET status=?,error=? WHERE id=?',
                                ('rejected' if getattr(error, 'rejected', False) else 'unknown', type(error).__name__, action_id))
            raise
        return dict(self.db.execute('SELECT * FROM outbound WHERE id=?', (action_id,)).fetchone())

    def status(self):
        return {'binding': self.config, 'offset': self.get('offset'), 'watch': self.get('watch'),
                'unpublished': self.db.execute('SELECT count(*) FROM inbound WHERE published=0').fetchone()[0],
                'uncertain_sends': [dict(row) for row in self.db.execute("SELECT id,status FROM outbound WHERE status IN ('started','unknown')")]}

    def close(self):
        self.db.close()

    def health(self):
        watch = self.get('watch', {})
        bad = time.time() - watch.get('checked_at', 0) > 180 or watch.get('state') != 'connected'
        previous = self.get('health_bad', False)
        if bad and not previous:
            atomic(self.home / 'inbox' / 'telegram-health.json',
                   {'channel': 'telegram', 'kind': 'listener-health', 'text': 'Telegram listener is unhealthy. Inspect telegram status and the user service; this is not an owner instruction.'})
        with self.db:
            self.set('health_bad', bad)
        return {'healthy': not bad, 'watch': watch}

    def watch(self):
        with (self.folder / 'listener.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            stopping = False
            def stop(*_):
                nonlocal stopping
                stopping = True
            signal.signal(signal.SIGTERM, stop)
            signal.signal(signal.SIGINT, stop)
            while not stopping:
                try:
                    self.poll()
                except Exception as error:
                    with self.db:
                        self.set('watch', {'state': 'error', 'checked_at': time.time(), 'error': type(error).__name__})
                    print('Telegram listener error: ' + type(error).__name__, file=sys.stderr, flush=True)
                    for _ in range(10):
                        if stopping:
                            break
                        time.sleep(1)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--home', default=str(Path.home()))
    sub = parser.add_subparsers(dest='command', required=True)
    for command in ('watch', 'status', 'health', 'check'):
        sub.add_parser(command)
    sub.add_parser('import-legacy').add_argument('database')
    send = sub.add_parser('send')
    send.add_argument('--id', required=True)
    send.add_argument('--reply-to', type=int)
    send.add_argument('--file', required=True, help='UTF-8 text file, or - for stdin')
    read = sub.add_parser('read')
    read.add_argument('--after', type=int, default=-1)
    read.add_argument('--limit', type=int, default=20)
    args = parser.parse_args()
    with contextlib.closing(Channel(args.home)) as channel:
        if args.command == 'watch':
            channel.watch()
            return
        if args.command == 'send':
            value = channel.send(args.id, sys.stdin.read() if args.file == '-' else Path(args.file).read_text(), args.reply_to)
        elif args.command == 'read':
            value = [json.loads(row[0]) for row in channel.db.execute('SELECT envelope FROM inbound WHERE update_id>? ORDER BY update_id LIMIT ?',
                      (args.after, max(1, min(args.limit, 100))))]
        elif args.command == 'import-legacy':
            channel.import_legacy(args.database)
            value = channel.status()
        elif args.command == 'check':
            identity = channel.api('getMe', {})
            webhook = channel.api('getWebhookInfo', {})
            if webhook.get('url'):
                raise ValueError('A webhook is configured; polling cannot start')
            value = {'bot_id': identity['id'], 'username': identity.get('username'), 'webhook': False}
        else:
            value = getattr(channel, args.command)()
        print(json.dumps(value, ensure_ascii=False))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(str(error) if isinstance(error, (APIError, ValueError)) else type(error).__name__, file=sys.stderr)
        sys.exit(1)
