package cli

// Usage lists the commands. Everything else an agent, or root, does with the
// harness is a file, described in the manual.
const Usage = `Usage:
  agent provider install NAME FILE  install provider plugin NAME from the downloaded FILE,
                                    verified against this release's checksums (root)
  agent help                        this text
  agent daemon                      run the service (root only)

Messages, subagents, journals, providers and the version are files,
described in /var/lib/agent/manual/manual.md.
`
