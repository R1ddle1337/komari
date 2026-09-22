package fs

import (
	"os"
	"testing"

	"github.com/dop251/goja"
)

func TestFSModeDistinguishesEncodingFromExplicitPermissions(t *testing.T) {
	vm := goja.New()
	cases := []struct {
		name       string
		expression string
		want       os.FileMode
	}{
		{"encoding string", "'utf8'", 0o666},
		{"other encoding", "'hex'", 0o666},
		{"empty string", "''", 0o666},
		{"encoding object", "({encoding:'utf8'})", 0o666},
		{"numeric mode object", "({encoding:'utf8',mode:0o640})", 0o640},
		{"octal mode object", "({mode:'0600'})", 0o600},
		{"invalid mode text", "({mode:'utf8'})", 0o666},
		{"number", "0o644", 0o644},
		{"octal string", "'0644'", 0o644},
		{"explicit zero", "0", 0},
		{"zero string", "'0000'", 0},
		{"zero mode object", "({mode:0})", 0},
		{"undefined", "undefined", 0o666},
		{"null", "null", 0o666},
		{"undefined object mode", "({mode:undefined})", 0o666},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := vm.RunString(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			if got := fsMode(value, 0o666); got != tc.want {
				t.Fatalf("fsMode(%s) = %#o, want %#o", tc.expression, got, tc.want)
			}
		})
	}
	if got := fsMode(nil, 0o777); got != 0o777 {
		t.Fatalf("nil mode = %#o, want directory fallback %#o", got, os.FileMode(0o777))
	}
}

func TestFSAsyncWriteEncodingKeepsFileReadable(t *testing.T) {
	root := t.TempDir()
	module, err := New(nil, root, root, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(module.Close)
	vm := goja.New()
	operation := module.prepareFSAsync(vm, "writeFile", []goja.Value{
		vm.ToValue("encoded.txt"), vm.ToValue("readable"), vm.ToValue("utf8"),
	})
	if _, err := operation.run(); err != nil {
		t.Fatal(err)
	}
	path, err := module.Resolve("encoded.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := module.nodeReadFile(path)
	if err != nil {
		t.Fatalf("encoding option created an unreadable file: %v", err)
	}
	if string(data) != "readable" {
		t.Fatalf("file contents = %q", data)
	}
}
