class Incoda < Formula
  desc "Keyed, machine-local job lane for builds, tests and AI-agent fleets"
  homepage "https://github.com/deblasis/incoda"
  version "0.4.0"
  license "MIT"

  on_arm do
    url "https://github.com/deblasis/incoda/releases/download/v0.4.0/incoda_darwin_arm64"
    sha256 "ed9ac67cd5aca3fca9f9c16f7dd1a81f54e6a2f4cf611e108ec4d690e30c3833"
  end
  on_intel do
    url "https://github.com/deblasis/incoda/releases/download/v0.4.0/incoda_darwin_amd64"
    sha256 "e2b321ebed8ffbf910a2eb44baa5e66303e8920fffbafbcc92492dbe0a9e2205"
  end

  def install
    if Hardware::CPU.arm?
      bin.install "incoda_darwin_arm64" => "incoda"
    else
      bin.install "incoda_darwin_amd64" => "incoda"
    end
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/incoda version")
  end
end
