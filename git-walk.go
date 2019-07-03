package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	getopt "github.com/pborman/getopt/v2"
)

const HELP = `
Run ` + "`command`" + ` in every git repo found. The command defaults to:

    git status --short -b

By default, the commands are run in parallel, and their stderr and stdout are
printed when the commmand completes, to avoid having the parallel command
output intermingled unintelligibly. Some commands only colorize when writing to
a terminal, in which case --serial may be useful, which runs the command with
output directly to the console at the price of being slower.

Examples:

    git-walk -p -q -- git describe
    git-walk -- git fetch --prune --all
    git-walk -- git co master
`

// XXX use pty to support colorization in parallel?
// - https://github.com/creack/pty

// Serialize writing of multi-line output so it is not interleaved.
var output sync.Mutex

func cwd() string {
	wd, _ := os.Getwd()
	return wd
}

func main() {
	var (
		help        = false
		debug       = false
		quiet       = false
		where       = cwd()
		serial      = false
		parallel    = true
		concurrency = 20
	)

	getopt.SetParameters("[-- command...]")
	getopt.FlagLong(&help, "help", 'h',
		"Print this helpful message and exit")
	getopt.FlagLong(&debug, "debug", 'd',
		"Print debug trace")
	getopt.FlagLong(&quiet, "quiet", 'q',
		"Do not print commands that are being run")
	getopt.FlagLong(&where, "where", 'w',
		"Look for git repos in `W` and below", "W")
	getopt.FlagLong(&serial, "serial", '1',
		"Run serially")
	getopt.FlagLong(&parallel, "parallel", 'p',
		"Run commands in parallel")
	getopt.Flag(&concurrency, 'n',
		"Run this many commmands in parallel", "CONCURENCY")
	getopt.Parse()
	cmd := getopt.Args()

	if serial {
		concurrency = 1
	}

	if len(cmd) < 1 {
		cmd = []string{"git", "status", "--short", "-b"}
	}

	log.SetFlags(log.Lshortfile)

	if !debug {
		log.SetOutput(ioutil.Discard)
	}

	log.Println("parallel", parallel)
	log.Println("concurrency", concurrency)
	log.Println("cmd", cmd)
	log.Printf("where %q\n", where)

	if help {
		getopt.PrintUsage(os.Stdout)
		fmt.Fprintf(os.Stdout, "%s", HELP)
		return
	}

	var wg sync.WaitGroup
	dirs := make(chan string)

	execute := func(dir string) {
		log.Println("execute where:", dir)
		child := exec.Command(cmd[0], cmd[1:]...)
		child.Dir = dir

		if concurrency == 1 {
			child.Stderr = os.Stderr
			child.Stdout = os.Stdout
		} else {
			child.Stderr = new(bytes.Buffer)
			child.Stdout = new(bytes.Buffer)
		}

		err := child.Run()

		output.Lock()
		defer output.Unlock()
		if err == nil {
			if !quiet {
				fmt.Printf("cd %s; %s\n", dir, strings.Join(cmd, " "))
			}

		} else if eexit, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "cd %s: `%s` failed on %v\n",
				dir, strings.Join(cmd, " "), eexit)

			// If child was signaled, self-terminate with the same signal.
			status, ok := eexit.Sys().(syscall.WaitStatus)
			self, _ := os.FindProcess(os.Getpid())
			if ok && status.Signaled() {
				self.Signal(status.Signal())
			}
		} else {
			fmt.Fprintf(os.Stderr, "cd %s: `%s` failed on %v\n",
				dir, strings.Join(cmd, " "), err)
		}
		if concurrency != 1 {
			os.Stdout.Write(child.Stdout.(*bytes.Buffer).Bytes())
			os.Stderr.Write(child.Stderr.(*bytes.Buffer).Bytes())
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			for dir := range dirs {
				execute(dir)
			}
			wg.Done()
		}()
	}

	walker := func(path string, info os.FileInfo, err error) (_ error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "walk %q failed with %v\n", path, err)
			return
		}
		if !info.IsDir() {
			return
		}
		infos, err := ioutil.ReadDir(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "readdir %q failed with %s\n", path, err)
			return
		}

		for _, info := range infos {
			if info.IsDir() && info.Name() == ".git" {
				dirs <- path
				return filepath.SkipDir
			}
		}
		return
	}
	filepath.Walk(where, walker)
	close(dirs)
	wg.Wait()
}
