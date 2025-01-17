package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/hashicorp/logutils"
	"gopkg.in/yaml.v2"
)

var (
	version   string
	buildDate string
)

func main() {
	confFile := flag.String("conf", "", "specify config file or S3 URL")
	domain := flag.String("domain", ".local", "reverse proxy suffix")
	var showVersion, showConfig, localMode bool
	var defaultPort int
	flag.BoolVar(&showVersion, "version", false, "show version")
	flag.BoolVar(&showVersion, "v", false, "show version")
	flag.BoolVar(&showConfig, "x", false, "show config")
	flag.BoolVar(&localMode, "local", false, "local mode (for development)")
	flag.IntVar(&defaultPort, "default-port", 80, "default port number")
	logLevel := flag.String("log-level", "info", "log level (trace, debug, info, warn, error)")
	flag.VisitAll(overrideWithEnv)
	flag.Parse()

	if showVersion {
		fmt.Printf("mirage %v (%v)\n", version, buildDate)
		return
	}

	filter := &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"trace", "debug", "info", "warn", "error"},
		MinLevel: logutils.LogLevel(*logLevel),
		Writer:   os.Stderr,
	}
	log.SetOutput(filter)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("[debug] setting log level: %s", *logLevel)

	cfg, err := NewConfig(&ConfigParams{
		Path:        *confFile,
		LocalMode:   localMode,
		Domain:      *domain,
		DefaultPort: defaultPort,
	})
	if err != nil {
		log.Fatalf("[error] %s", err)
	}
	if showConfig {
		yaml.NewEncoder(os.Stdout).Encode(cfg)
		return
	}
	Setup(cfg)
	Run()
}

func overrideWithEnv(f *flag.Flag) {
	name := strings.ToUpper(f.Name)
	name = strings.Replace(name, "-", "_", -1)
	if s := os.Getenv("MIRAGE_" + name); s != "" {
		f.Value.Set(s)
	}
}
