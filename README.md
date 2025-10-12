# Yet Another Speedtest CLI for Cloudflare

Hey there! 👋

This project is inspired by and based on the awesome work from [`KNawm/speed-cloudflare-cli`](https://github.com/KNawm/speed-cloudflare-cli). Huge thanks to them for their code, logic, and all the effort that made this possible!

I'm pretty new to Golang, and I use tools like this every day to check my internet health at work. Rebuilding it in Go started as a way for me to practice and learn, but I hope it helps others too.

If you spot any bugs or have ideas to improve the code, feel free to open an issue or send a PR. Thanks for checking this out!

## How to use

### CLI usage

```bash
## Download speedtest
go-speed-cloudflare-cli -download

## Upload speedtest
go-speed-cloudflare-cli -upload 

## Lite mode (10MB download/upload tests)
go-speed-cloudflare-cli -lite

## Lite download mode (10MB download tests)
go-speed-cloudflare-cli -lite-download

## Lite upload mode (10MB upload tests)
go-speed-cloudflare-cli -lite-upload

## All in one
go-speed-cloudflare-cli

## Show version
go-speed-cloudflare-cli -version

## Show help
go-speed-cloudflare-cli -help
```

### Docker

```bash
docker run -it --rm nrt.vultrcr.com/edoo/go-speed-cloudflare-cli:alpine
```

## CLI Flags

| Flag            | Description                                 |
|-----------------|---------------------------------------------|
| --download      | Test download speed only                    |
| --upload        | Test upload speed only                      |
| --version       | Show version and exit                       |
| --lite          | Run only up to 10MB download/upload tests   |
| --lite-download | Run only up to 10MB download tests          |
| --lite-upload   | Run only up to 10MB upload tests            |


### Build

#### Build locally

```bash
cd src
go mod init
go mod tidy

## Build for linux x86_64
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ../go-speed-cloudflare-cli-linux-amd64 .

## Build for darwin x86_64
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o ../go-speed-cloudflare-cli-darwin-amd64 .

## Build for windows x86_64
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o ../go-speed-cloudflare-cli-windows-amd64.exe .
```

#### Alpine

```
docker build --target final-alpine -t nrt.vultrcr.com/edoo/go-speed-cloudflare-cli:alpine . --push
```

#### Debian slim

```
docker build --target final-slim -t nrt.vultrcr.com/edoo/go-speed-cloudflare-cli:slim . --push
```

## Git config on your local device

```bash
git config --local user.name "zoonderkins"

git config --local user.email "xxxx@xxx.com"
```