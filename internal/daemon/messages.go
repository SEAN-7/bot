package daemon

// SystemPrompt is used verbatim, as the first and most stable system segment.
const SystemPrompt = `You live on this machine. Your only tool is the shell. ~/README.md and
~/TASK.md are loaded every time you start, and you may edit them. Messages
arrive as files in ~/inbox/. ~/journal/ is a read-only record of everything
you've done. When a run ends or your context resets, you keep only what's
on disk. To stop, reply without a command.`

const (
	MsgInterrupted         = "You were interrupted. Resume from TASK.md."
	MsgResetFmt            = "Context reset. Continue from TASK.md. Previous journal: %s"
	MsgWarn80              = "Context is 80% full. Save your state to TASK.md and notes now."
	MsgTimeoutFmt          = "Command timed out after %s; its process group was killed. Give it a longer wait (up to %s), or run it in the background with systemd-run --user."
	MsgOutputSavedFmt      = "[full output: %s]"
	MsgInboxClipFmt        = "[file clipped; full file: %s]"
	MsgGuardFmt            = "Context is over 95%% full at the start of a run (%d of %d tokens); not starting. Reduce README.md, TASK.md or inbox files."
	MsgOverflowGuardFmt    = "Context is over the model's limit at the start of a run; not starting. Reduce README.md, TASK.md or inbox files. Provider: %v"
	MsgProviderFmt         = "Provider failing since %s: %s"
	MsgConfigPrefix        = "config.json: "
	MsgSubagentFmt         = "Subagent %s did not start: %s"
	MsgLimitFmt            = "Agent limit reached (max_agents=%d); this agent was not started."
	MsgLimitParentFmt      = "Subagent %s was not started: agent limit reached (max_agents=%d)."
	MsgNoFreeUser          = "No free agent user: the free users' homes belong to agents that have not been recovered; this agent was not started."
	MsgNoFreeUserParentFmt = "Subagent %s was not started: no free agent user (their homes belong to agents that have not been recovered)."
	MsgConfigTooLarge      = "larger than 1 MiB" // after MsgConfigPrefix
	MsgUndeliveredFmt      = "Message to %s dropped: there is no subagent by that name."
	MsgImageShownFmt       = "[%s %dx%d, %d bytes, shown below]"
	MsgImageTooLargeFmt    = "[image not shown: %d bytes is over the 5 MiB limit; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageTooWideFmt     = "[image not shown: %dx%d is over 2000 px a side; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageUndecodableFmt = "[image not shown: could not decode it: %v. File: %s]"
	MsgImageTypeFmt        = "[image not shown: %s is not supported, only JPEG, PNG and GIF; convert it and copy the result into ~/inbox/. File: %s]"
	MsgImageNoModelFmt     = "[image not shown: the model does not accept images. File: %s]"
	MsgImageJournalFmt     = "[image not shown: its journal copy could not be saved: %v. File: %s]"
	MsgImageBudgetFmt      = "[image not shown: images in this conversation would exceed 20 MiB; resize it, or copy it into ~/inbox/ again after this conversation ends. File: %s]"
	MsgBinaryFmt           = "[binary file not shown: %s, %d bytes. File: %s]"
)
