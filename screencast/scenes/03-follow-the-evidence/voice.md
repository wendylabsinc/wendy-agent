A run writes an attempt directory under Build slash e2e. Start with the
recording for the failed behavior.

`recording.md` shows the command, exit status, stdout, and stderr.
`recording.sh.txt` replays the captured shell calls when you need to reproduce
the failure manually. The xUnit and JSON files are there for tools and CI.

The important point is that a failure leaves the exact evidence you need; you
do not have to reconstruct the command from a log fragment.
