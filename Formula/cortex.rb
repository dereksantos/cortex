# Homebrew Formula for Cortex
# Usage: brew install --HEAD https://raw.githubusercontent.com/dereksantos/cortex/main/Formula/cortex.rb
#
# NOTE: A tagged release has not been cut yet. Only `--HEAD` installs are supported.
# Once v0.1.0 is tagged, fill in the sha256 below with the tarball's sha256.

class Cortex < Formula
  desc "Context memory for AI development - never lose a decision again"
  homepage "https://github.com/dereksantos/cortex"
  head "https://github.com/dereksantos/cortex.git", branch: "main"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}"), "./cmd/cortex"
  end

  def caveats
    <<~EOS
      🤖 Cortex installed successfully!

      📖 Quick Start:
        1. cd your-project
        2. cortex init --auto
        3. cortex daemon

      💡 Requirements:
        - Ollama (https://ollama.ai) for LLM analysis
        - Claude Code or other AI coding tool

      🔍 Commands:
        cortex help           Show all commands
        cortex search <q>     Search your context
        cortex insights       View extracted insights
        cortex entities       Browse entities
    EOS
  end

  test do
    assert_match "cortex version", shell_output("#{bin}/cortex version")
  end
end
