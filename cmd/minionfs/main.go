package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	minionfs "github.com/Prana-vvb/minionfs/internal/fs"
)

type config struct {
	debug      bool
	lowerDir   string
	upperDir   string
	mount      string
	encryptKey string
	compress   bool
}

func main() {
	debug := flag.Bool("d", false, "Enable debug mode")
	encryptKey := flag.String("encrypt-key", "", "AES-256-GCM encryption passphrase for the upper layer")
	compress := flag.Bool("compress", false, "Enable gzip compression for the upper layer")
	flag.Parse()

	if *encryptKey != "" && *compress {
		log.Fatal("--encrypt-key and --compress are mutually exclusive")
	}

	if flag.NArg() < 3 {
		fmt.Println("usage: minionfs [-d] [--encrypt-key=<key>] [--compress] <lowerdir> <upperdir> <mountpoint>")
		return
	}

	cfg := &config{
		debug:      *debug,
		lowerDir:   flag.Arg(0),
		upperDir:   flag.Arg(1),
		mount:      flag.Arg(2),
		encryptKey: *encryptKey,
		compress:   *compress,
	}

	// Make sure upper and lower dirs actually exist
	for _, dir := range []string{cfg.lowerDir, cfg.upperDir} {
		if _, err := os.Stat(dir); err != nil {
			log.Fatalf("Directory does not exist: %s", dir)
		}
	}

	// Build the codec based on flags (nil → PlainCodec inside FS)
	var codec minionfs.FileCodec
	switch {
	case cfg.encryptKey != "":
		codec = minionfs.NewAESCodec(cfg.encryptKey)
		log.Println("AES-256-GCM encryption enabled")
	case cfg.compress:
		codec = minionfs.GzipCodec{}
		log.Println("Gzip compression enabled")
	default:
		log.Println("No encoding — upper layer stored as plaintext")
	}

	if cfg.debug {
		log.Println("Debug mode enabled")
		log.Printf("Lower dir: %s", cfg.lowerDir)
		log.Printf("Upper dir: %s", cfg.upperDir)
		log.Printf("Mountpoint: %s", cfg.mount)
	}

	c, err := fuse.Mount(cfg.mount)
	if err != nil {
		log.Println(err)
		return
	}
	defer c.Close()

	serv := make(chan error, 1)
	go func() {
		serv <- fs.Serve(c, &minionfs.FS{
			Debug:    cfg.debug,
			UpperDir: cfg.upperDir,
			LowerDir: cfg.lowerDir,
			Codec:    codec,
		})
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)

	<-signals
	log.Println("Interrupt received: shutting down.")
	unmount_err := fuse.Unmount(cfg.mount)

	if unmount_err != nil {
		log.Println("Lazy Unmounting")
		command := exec.Command("fusermount", "-u", "-z", cfg.mount)
		cmd_err := command.Run()

		if cmd_err != nil {
			log.Println(cmd_err)
			return
		}

		return
	}

	if err := <-serv; err != nil {
		log.Println("Serve error:", err)
	}
}
