Renders a TUI dashboard for your device. This shows all apps, including their active status (running/stopped), OTel metrics and logs in real-time.

The status line at the bottom summarizes the app counts, for example:

```
  3 apps  ● 2 running  ○ 1 stopped  (refreshes every 2s)
```

When one or more apps are crash-looping, a `↻ N crash-looping` segment is appended (it is omitted when the count is zero):

```
  3 apps  ● 1 running  ○ 1 stopped  ↻ 1 crash-looping  (refreshes every 2s)
```