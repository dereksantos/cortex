# Records docs/assets/demo.gif: Cortex finds and fixes a bug in a small
# project (docs/assets/demo/testdata/slug), with a real model.
#
# To record it again, run this from the repository root:
#
#   vhs docs/assets/demo/demo.tape
#
# Requirements: vhs, `cortex` on PATH, and a configured backend (the GIF was
# recorded with qwen3.8-27b on a local server). The tape copies the
# fixture to ~/demo/slug and replaces that directory on each run. It adds
# ~/demo/slug to your trusted workspaces (`cortex project trust add`), so the
# post-edit format hook runs on the edit. Undo it with
# `cortex project trust remove ~/demo/slug`.
#
# The setup also creates ~/demo/slug/.cortex. Without it, Cortex walks up
# from ~/demo/slug, finds ~/.cortex, and uses your user-level data (notes,
# sessions) for the demo session.

Output docs/assets/demo.gif

Require cortex
Require git

Set Shell zsh
Set FontSize 16
Set Width 1280
Set Height 760
Set Padding 24
Set TypingSpeed 35ms
Set PlaybackSpeed 1.5
Set Theme "Catppuccin Mocha"

Hide
Type "PS1='$ ' && rm -rf ~/demo/slug && mkdir -p ~/demo && cp -R docs/assets/demo/testdata/slug ~/demo/slug && cd ~/demo/slug && go mod init example.com/slug > /dev/null 2>&1 && mkdir .cortex && echo '*' > .cortex/.gitignore && git init -q && git add -A && git commit -qm init && cortex project trust add ~/demo/slug > /dev/null && clear"
Enter
Sleep 1s
Show

Type "cortex"
Enter
Sleep 3s
Type "The tests fail. Find the bug, fix it, and run the tests."
Sleep 500ms
Enter

# The status row shows "*" while the turn runs and "." when it is idle.
Sleep 5s
Wait+Line@240s /\. ❯/
Sleep 6s
