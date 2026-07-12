const methods = [
  ["linux", "amd64", [deb]],
  ["linux", "arm64", [deb]],
];

const packages = methods.map(([goos, goarch, methods]) => {
  return methods.map(method => {
    const exe = goos == "windows" ? ".exe" : "";

    method.build({
      name: "gubalctl",
      description: "The CLI for gubald",
      homepage: "https://anubis.techaro.lol",
      license: "MIT",
      platform: goos,
      goarch,

      build: ({ bin, etc, systemd, doc }) => {
        $`go build -trimpath -o ${bin}/gubalctl${exe} -ldflags '-s -w -extldflags "-static"' ./cmd/gubalctl`;
      },
    });
  });
});