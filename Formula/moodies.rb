class Moodies < Formula
  desc "Local proxy agent that captures AI API traffic (Claude, OpenAI, Gemini, and more)"
  homepage "https://github.com/fof-headless/moodies"
  url "https://github.com/fof-headless/moodies/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "579a0f61f5cea24a09ad90556c2636f01cd4378774e1a628244a4a07d5fef15c"
  license :cannot_represent
  head "https://github.com/fof-headless/moodies.git", branch: "main"

  depends_on "go" => :build
  # mitmproxy is no longer needed — the proxy is now a pure-Go embedded binary.

  def install
    ldflags = %W[
      -s -w
      -X main.version=#{version}
    ]

    system "go", "build", *std_go_args(output: bin/"moodies", ldflags: ldflags), "./cmd/doomsday"
    system "go", "build", *std_go_args(output: bin/"moodies-daemon", ldflags: ldflags), "./cmd/doomsday-daemon"
    system "go", "build", *std_go_args(output: bin/"moodies-disable", ldflags: ldflags), "./cmd/doomsday-disable"
    system "go", "build", *std_go_args(output: bin/"moodies-claude", ldflags: ldflags), "./cmd/moodies-claude"
  end

  def caveats
    <<~EOS
      First-time setup:
        moodies install

      Config and runtime data live in ~/.doomsday/ (legacy path; will move in a future release).
    EOS
  end

  test do
    assert_match "Doomsday agent CLI", shell_output("#{bin}/moodies --help")
  end
end
