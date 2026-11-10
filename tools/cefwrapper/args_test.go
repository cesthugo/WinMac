package main

import "testing"

func TestArguments(t *testing.T) {
	cases := map[string]string{
		`"C:\Program Files (x86)\Steam\bin\cef\cef.win64\steamwebhelper.exe" -lang=en --type=gpu`: "-lang=en --type=gpu",
		`steamwebhelper.exe  "--a=b c" -x`: `"--a=b c" -x`,
		`steamwebhelper.exe`:               "",
		`"C:\has space\x.exe"`:             "",
	}
	for in, want := range cases {
		if got := Arguments(in); got != want {
			t.Errorf("Arguments(%q) = %q, want %q", in, got, want)
		}
	}
}
