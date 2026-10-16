package daemon

// SystemPrompt is used verbatim, as the first and most stable system segment.
const SystemPrompt = `You live on this machine. Your only tool is the shell. ~/README.md is
loaded every time you start, and you may edit it. Messages arrive as
files in ~/inbox/. ~/journal/ is a read-only record of everything you've
done, and ~/manual/ describes how this machine and its harness work.
When a run ends or your context resets, you keep only what's on disk. To
stop, reply without a command. That reply goes only to your journal; it
is not sent to anyone.`

const (
	MsgInterruptedFmt      = "You were interrupted. Interrupted journal: %s"
	MsgResetFmt            = "Context reset. Previous journal: %s"
	MsgTimeoutFmt          = "Command timed out after %s; its process group was killed. Give it a longer wait (up to %s), or run it in the background with systemd-run --user."
	MsgOutputClipped       = "[output clipped; redirect long output to a file to read all of it]"
	MsgStraysFmt           = "Killed %d %s this command left running (%s). A command's processes end with it; start anything that should keep running with systemd-run --user."
	MsgInboxClipFmt        = "[file clipped; full file: %s]"
	MsgOverflowGuardFmt    = "Context is over the model's limit at the start of a run; not starting. Reduce README.md or the inbox files. Provider: %v"
	MsgProviderFmt         = "Provider failing since %s: %s"
	MsgConfigPrefix        = "config.json: "
	MsgSubagentFmt         = "Subagent %s did not start: %s"
	MsgLimitFmt            = "Agent limit reached (max_agents=%d); this agent was not started."
	MsgLimitParentFmt      = "Subagent %s was not started: agent limit reached (max_agents=%d)."
	MsgConfigTooLarge      = "larger than 1 MiB" // after MsgConfigPrefix
	MsgUndeliveredFmt      = "Message to %s dropped: there is no subagent by that name."
	MsgImageShownFmt       = "[%s %dx%d, %d bytes, shown below]"
	MsgImageTooLargeFmt    = "[image not shown: %d bytes is over the 5 MiB limit; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageTooWideFmt     = "[image not shown: %dx%d is over 2000 px a side; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageUndecodableFmt = "[image not shown: could not decode it: %v. File: %s]"
	MsgImageTypeFmt        = "[image not shown: %s is not supported, only %s; convert it and copy the result into ~/inbox/. File: %s]"
	MsgImageNoModelFmt     = "[image not shown: the model does not accept images. File: %s]"
	MsgImageJournalFmt     = "[image not shown: its journal copy could not be saved: %v. File: %s]"
	MsgImageBudgetFmt      = "[image not shown: images in this run would exceed 20 MiB; resize it, or copy it into ~/inbox/ again after this run ends. File: %s]"
	MsgBinaryFmt           = "[binary file not shown: %s, %d bytes. File: %s]"
	MsgUpgradeFmt          = "The harness was upgraded from v%s to v%s. What changed for you, newest first (~/manual/notes.md has every release):\n\n%s"
	MsgUpgradeSinceFmt     = "The harness was upgraded to v%s. What changed for you since v%s, newest first (~/manual/notes.md has every release):\n\n%s"
)
