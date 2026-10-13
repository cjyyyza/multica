package p4cache

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFinishChangeReplacesAndRemovesOldShelves(t *testing.T) {
	buildDir := t.TempDir()
	source := filepath.Join(buildDir, "fake.go")
	program := `package main
import("encoding/json";"fmt";"os";"strings")
type state struct{ Open []string; Shelf []string }
func main(){
    path:=os.Getenv("P4_REVIEW_STATE")
    raw,_:=os.ReadFile(path)
    var s state
    json.Unmarshal(raw,&s)
    args:=os.Args[1:]
    for len(args)>0 {
        if args[0]=="-ztag"{args=args[1:];continue}
        if args[0]=="-p"||args[0]=="-u"||args[0]=="-c"||args[0]=="-C"{args=args[2:];continue}
        break
    }
    if len(args)==0{panic("missing verb")}
    switch args[0] {
    case "revert":
    case "opened","fstat":
        for _,f:=range s.Open{fmt.Printf("... depotFile %s\n... clientFile X\n... action edit\n... change 1001\n... headType text\n... headRev 1\n... haveRev 1\n\n",f)}
    case "shelve":
        flags:=" "+strings.Join(args," ")+" "
        if strings.Contains(flags," -d "){s.Shelf=nil
        }else if strings.Contains(flags," -r "){s.Shelf=append([]string{},s.Open...)
        }else{
            for _,f:=range s.Open{
                found:=false
                for _,old:=range s.Shelf{if f==old{found=true}}
                if !found{s.Shelf=append(s.Shelf,f)}
            }
        }
    case "change":
        if len(s.Shelf)>0{fmt.Fprintln(os.Stderr,"Change 1001 has shelved files associated with it and can't be deleted.");os.Exit(1)}
    default: panic("unexpected "+strings.Join(args," "))
    }
    next,_:=json.Marshal(s);os.WriteFile(path,next,0600)
}
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(buildDir, "fake")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	type shelfState struct {
		Open  []string
		Shelf []string
	}
	for _, tc := range []struct {
		name   string
		opened []string
	}{
		{"drop reverted file from reused shelf", []string{"//depot/Dev/A.cpp"}},
		{"delete a previously shelved empty changelist", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			initial, _ := json.Marshal(shelfState{Open: tc.opened, Shelf: []string{"//depot/Dev/A.cpp", "//depot/Dev/B.cpp"}})
			os.WriteFile(path, initial, 0600)
			t.Setenv("P4_REVIEW_STATE", path)
			c := &Cache{P4Path: bin}
			ws := &Workspace{Port: "p4.example:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root}
			result, err := c.FinishChange(t.Context(), ws, "1001")
			if err != nil {
				t.Fatalf("finish did not clean up existing shelf: %v", err)
			}
			raw, _ := os.ReadFile(path)
			var got shelfState
			json.Unmarshal(raw, &got)
			if len(got.Shelf) != len(tc.opened) {
				t.Errorf("shelf still has %v, but changelist has %v; reported file count=%d", got.Shelf, tc.opened, len(result.Files))
			}
		})
	}
}
