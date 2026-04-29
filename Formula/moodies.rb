class Moodies < Formula
  desc "Local proxy agent that captures and sanitizes Claude.ai traffic"
  homepage "https://github.com/fof-headless/moodies"
  url "https://github.com/fof-headless/moodies/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_SHA256_OF_RELEASE_TARBALL"
  license :cannot_represent
  head "https://github.com/fof-headless/moodies.git", branch: "main"

  depends_on "go" => :build
  depends_on "python@3.12"

  def install
    ldflags = %W[
      -s -w
      -X main.version=#{version}
    ]

    system "go", "build", *std_go_args(output: bin/"moodies", ldflags: ldflags), "./cmd/doomsday"
    system "go", "build", *std_go_args(output: bin/"moodies-daemon", ldflags: ldflags), "./cmd/doomsday-daemon"
    system "go", "build", *std_go_args(output: bin/"moodies-disable", ldflags: ldflags), "./cmd/doomsday-disable"

    libexec.install "sanitizer"
  end

  def caveats
    <<~EOS
      Sanitizer script bundled at:
        #{libexec}/sanitizer/sanitizer.py

      First-time setup:
        moodies install

      Config and runtime data live in ~/.doomsday/ (legacy path; will move in a future release).
    EOS
  end

  test do
    assert_match "Doomsday agent CLI", shell_output("#{bin}/moodies --help")
  end
end
