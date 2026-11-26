class GutterServer < Formula
  desc "Self-hosted comic library server for the Gutter reader"
  homepage "https://github.com/rthaines211/gutter-server"
  url "https://github.com/rthaines211/gutter-server/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "8683299b4bd9ed8cdaaa82458568a41348840b93d17b294395ccef08864f7d11"
  license "AGPL-3.0-only"
  head "https://github.com/rthaines211/gutter-server.git", branch: "main"

  livecheck do
    url :stable
    strategy :github_latest
  end

  depends_on "go" => :build

  def fetch
    system "go", "mod", "download"
  end

  def install
    # go-sqlite3 and go-unarr need cgo.
    ENV["CGO_ENABLED"] = "1"
    # Workaround to avoid patchelf corruption when cgo is required
    if OS.linux? && Hardware::CPU.arm64?
      ENV["GO_EXTLINK_ENABLED"] = "1"
      ENV.append "GOFLAGS", "-buildmode=pie"
    end

    ldflags = "-X github.com/rthaines211/gutter-server/server/admin.Version=#{version}"
    system "go", "build", *std_go_args(ldflags:, output: bin/"gutter-server"), "."

    (buildpath/"gutter-server.yaml").write <<~YAML
      # Gutter Server config for Homebrew. Edit, then: brew services restart gutter-server
      port: 8081

      # The folder that holds one folder per series, CBZ/CBR files inside.
      comic_dirs:
        - /path/to/your/comics

      db_path: #{var}/gutter-server/gutter.db

      # Required. Sign in with any username and this password.
      # password: ""
    YAML
    etc.install "gutter-server.yaml"
    pkgshare.install "config.example.yaml"
  end

  post_install_steps do
    mkdir_p "gutter-server", base: :var
    mkdir_p "log", base: :var
  end

  def caveats
    <<~EOS
      Before starting the service, edit #{etc}/gutter-server.yaml:
        set comic_dirs to your comics folder, and uncomment password.
      The server refuses to start without a password.

      Then open http://localhost:8081 and add http://<this Mac>:8081 in Gutter.
      Every config key is listed in #{opt_pkgshare}/config.example.yaml.

      Chapter detection reads page text with tesseract: brew install tesseract
    EOS
  end

  service do
    run [opt_bin/"gutter-server", "-config", etc/"gutter-server.yaml"]
    environment_variables PATH: std_service_path_env
    keep_alive true
    working_dir var/"gutter-server"
    log_path var/"log/gutter-server.log"
    error_log_path var/"log/gutter-server.log"
  end

  test do
    port = free_port
    (testpath/"comics/Test Series").mkpath
    (testpath/"config.yaml").write <<~YAML
      port: #{port}
      comic_dirs:
        - #{testpath}/comics
      db_path: #{testpath}/gutter.db
    YAML

    # Refuses to start without a password.
    shell_output("#{bin}/gutter-server -config #{testpath}/config.yaml 2>&1", 1)

    ENV["GUTTER_PASSWORD"] = "brewtest"
    with_test_process(bin/"gutter-server", "-config", testpath/"config.yaml") do
      output = shell_output("curl --silent --retry 10 --retry-connrefused http://127.0.0.1:#{port}/healthz")
      assert_match "ok", output
      assert_equal "401", shell_output("curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:#{port}/library")
      assert_match version.to_s, shell_output("curl -s -u x:brewtest http://127.0.0.1:#{port}/")
    end
  end
end
