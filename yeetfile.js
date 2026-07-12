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
      version: "0.0.0",

      build: ({ bin, etc, systemd, doc }) => {
        $`go build -trimpath -o ${bin}/gubalctl${exe} -ldflags '-s -w -extldflags "-static"' ./cmd/gubalctl`;
      },
    });
  });
});

$`AWS_PROFILE=tigris aws s3 cp ./var/gubalctl_0.0.0_amd64.deb s3://xedn/dl/gubalctl/gubalctl_0.0.0_amd64.deb`;
$`AWS_PROFILE=tigris aws s3 cp ./var/gubalctl_0.0.0_arm64.deb s3://xedn/dl/gubalctl/gubalctl_0.0.0_arm64.deb`;