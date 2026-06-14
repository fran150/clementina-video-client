package config

import (
	"flag"
	"fmt"
	"net"
	"time"
)

const (
	defaultServerAddress = "127.0.0.1:6502"
	defaultBindAddress   = ":0"
	defaultRequestFPS    = 25
	defaultScale         = 4
)

type Config struct {
	ServerAddress      string
	BindAddress        string
	InputEnabled       bool
	InputServerAddress string
	InputBindAddress   string
	DisableKeyboard    bool
	DisableMouse       bool
	DisableGamepads    bool
	RequestFPS         int
	RepairTimeout      time.Duration
	NoResponseRetries  int
	Scale              int
	LogFrames          bool
	Fullscreen         bool
	DebugOverlay       bool
	Title              string
}

func Parse(args []string) (Config, error) {
	cfg := Config{
		ServerAddress:     defaultServerAddress,
		BindAddress:       defaultBindAddress,
		InputEnabled:      true,
		InputBindAddress:  defaultBindAddress,
		RequestFPS:        defaultRequestFPS,
		RepairTimeout:     100 * time.Millisecond,
		NoResponseRetries: 3,
		Scale:             defaultScale,
		DebugOverlay:      false,
		Title:             "Clementina Video",
	}

	flags := flag.NewFlagSet("clementina-video-client", flag.ContinueOnError)
	flags.StringVar(&cfg.ServerAddress, "server", cfg.ServerAddress, "MIA UDP endpoint")
	flags.StringVar(&cfg.BindAddress, "bind", cfg.BindAddress, "local UDP bind address")
	flags.BoolVar(&cfg.InputEnabled, "input", cfg.InputEnabled, "enable Wi-Fi input client")
	flags.StringVar(&cfg.InputServerAddress, "input-server", cfg.InputServerAddress, "MIA input UDP endpoint")
	flags.StringVar(&cfg.InputBindAddress, "input-bind", cfg.InputBindAddress, "local input UDP bind address")
	flags.BoolVar(&cfg.DisableKeyboard, "disable-keyboard", cfg.DisableKeyboard, "do not forward keyboard input")
	flags.BoolVar(&cfg.DisableMouse, "disable-mouse", cfg.DisableMouse, "do not forward mouse input")
	flags.BoolVar(&cfg.DisableGamepads, "disable-gamepads", cfg.DisableGamepads, "do not forward gamepad input")
	flags.IntVar(&cfg.RequestFPS, "fps", cfg.RequestFPS, "frame request cadence")
	flags.DurationVar(&cfg.RepairTimeout, "repair-timeout", cfg.RepairTimeout, "quiet time before NACK/retry")
	flags.IntVar(&cfg.NoResponseRetries, "no-response-retries", cfg.NoResponseRetries, "request retries before reconnect")
	flags.IntVar(&cfg.Scale, "scale", cfg.Scale, "initial integer window scale")
	flags.BoolVar(&cfg.LogFrames, "log-frames", cfg.LogFrames, "log applied frame updates to frame_updates.log")
	flags.BoolVar(&cfg.Fullscreen, "fullscreen", cfg.Fullscreen, "start in fullscreen")
	flags.BoolVar(&cfg.DebugOverlay, "debug-overlay", cfg.DebugOverlay, "show connection and frame-rate overlay")
	flags.StringVar(&cfg.Title, "title", cfg.Title, "window title")

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.RequestFPS < 1 || cfg.RequestFPS > 120 {
		return Config{}, fmt.Errorf("fps must be between 1 and 120")
	}
	if cfg.RepairTimeout <= 0 {
		return Config{}, fmt.Errorf("repair-timeout must be positive")
	}
	if cfg.NoResponseRetries < 1 {
		return Config{}, fmt.Errorf("no-response-retries must be at least 1")
	}
	if cfg.Scale < 1 {
		return Config{}, fmt.Errorf("scale must be at least 1")
	}
	if cfg.ServerAddress == "" {
		return Config{}, fmt.Errorf("server must not be empty")
	}
	if cfg.BindAddress == "" {
		return Config{}, fmt.Errorf("bind must not be empty")
	}
	if cfg.InputEnabled {
		if cfg.InputServerAddress == "" {
			inputAddress, err := defaultInputServerAddress(cfg.ServerAddress)
			if err != nil {
				return Config{}, err
			}
			cfg.InputServerAddress = inputAddress
		}
		if cfg.InputBindAddress == "" {
			return Config{}, fmt.Errorf("input-bind must not be empty")
		}
	}

	return cfg, nil
}

func defaultInputServerAddress(videoAddress string) (string, error) {
	host, _, err := net.SplitHostPort(videoAddress)
	if err != nil {
		return "", fmt.Errorf("server must include host and port: %w", err)
	}
	return net.JoinHostPort(host, "6503"), nil
}
