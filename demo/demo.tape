# Tiimi demo, recorded by demo/record.sh on a sample project (made-up logs).
Output demo/tiimi-demo.gif
Output demo/tiimi-demo.mp4

Set Shell "bash"
Set FontSize 17
Set Width 1280
Set Height 720
Set Padding 28
Set Theme "Catppuccin Mocha"
Set TypingSpeed 32ms
Set WindowBar Colorful

Hide
Type "source $TK_DEMO_ENV && clear" Enter
Sleep 500ms
Show

Type "# What did my coding agents cost last month, per issue?" Sleep 300ms Enter
Type "tk receipt" Sleep 400ms Enter
Sleep 6s

Hide
Type "clear" Enter
Show
Type "# One bug cost $96 across 7 sessions. Track the next one properly:" Sleep 300ms Enter
Type "tk init" Sleep 300ms Enter
Sleep 3s
Type `tk new "Fix refund rounding" -- "Half-cent refunds round down"` Sleep 300ms Enter
Sleep 2s

Hide
Type "clear" Enter
Show
Type "# Claude Code starts on it (its session-start hook shows this brief):" Sleep 300ms Enter
Type "tk start LOC-1" Sleep 300ms Enter
Sleep 2.5s
Hide
Type "seed claude" Enter
Sleep 800ms
Show
Type "# ...runs out of context, so it hands off:" Sleep 300ms Enter
Type `tk handoff "Rounding fixed in refunds.ts. Left: 2 failing tax.spec cases. Decimal.js everywhere was 3x slower, reverted."` Sleep 400ms Enter
Sleep 3.5s

Hide
Type "clear" Enter
Show
Type "# Next session, Codex picks it up, starting from the handoff:" Sleep 300ms Enter
Type "tk start LOC-1" Sleep 300ms Enter
Sleep 4.5s
Hide
Type "seed codex" Enter
Sleep 800ms
Show
Type `tk done "Half-even rounding in refunds and tax; tests green"` Sleep 300ms Enter
Sleep 3.5s

Type "tk board" Sleep 300ms Enter
Sleep 4.5s
Type "q"
Sleep 1.5s
