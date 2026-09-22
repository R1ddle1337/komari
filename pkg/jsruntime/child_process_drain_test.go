package jsruntime

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSpawnCloseWaitsForBothOutputStreams(t *testing.T) {
	jsRuntime, err := New(`
		async function verify(command, environment) {
			for (let attempt = 0; attempt < 8; attempt++) {
				await new Promise((resolve, reject) => {
					const child = require("child_process").spawn(command, ["-test.run=^TestChildProcessDrainHelper$"], {
						env: environment, encoding: "utf8"
					});
					let stdout = "", stderr = "";
					let stdoutEnded = false, stderrEnded = false;
					child.stdout.on("data", chunk => stdout += String(chunk));
					child.stderr.on("data", chunk => stderr += String(chunk));
					child.stdout.on("end", () => stdoutEnded = true);
					child.stderr.on("end", () => stderrEnded = true);
					child.on("error", reject);
					child.on("close", code => {
						if (code === 0 && stdout === "o".repeat(128 * 1024) && stderr === "e".repeat(64 * 1024) && stdoutEnded && stderrEnded) {
							resolve();
						} else {
							reject(new Error(JSON.stringify({ attempt, code, stdoutLength: stdout.length, stderrLength: stderr.length, stdoutEnded, stderrEnded })));
						}
					});
				});
			}
			return true;
		}
	`, Options{NodeJS: true, AllowExec: true, BaseDir: t.TempDir(), Console: io.Discard, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer jsRuntime.Close()
	environment := childHelperEnvironment("drain-output")
	if err := jsRuntime.Call("verify", os.Args[0], environment); err != nil {
		t.Fatalf("spawn close overtook child output: %v", err)
	}
}

func TestChildProcessDrainHelper(t *testing.T) {
	if os.Getenv(childHelperModeEnvironment) != "drain-output" {
		return
	}
	_, _ = io.WriteString(os.Stdout, strings.Repeat("o", 128*1024))
	_, _ = io.WriteString(os.Stderr, strings.Repeat("e", 64*1024))
	os.Exit(0)
}
