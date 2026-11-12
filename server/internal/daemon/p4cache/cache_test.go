package p4cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFakeP4(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "fake_p4.go")
	program := `package main
import("fmt";"os";"strings")
func main(){
 joined:=" "+strings.Join(os.Args[1:]," ")+" "
 if log:=os.Getenv("P4_CONTENT_LOG");log!=""{ f,_:=os.OpenFile(log,os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);fmt.Fprintln(f,joined+" P4DIFF="+os.Getenv("P4DIFF"));f.Close() }
 for _,verb:=range []string{"diff2","diff","print","client","where"}{if strings.Contains(joined," "+verb+" "){fmt.Print(os.Getenv("P4_CONTENT_"+strings.ToUpper(verb)));return}}
 if strings.Contains(joined," info "){fmt.Println("User name: testuser");return}
 if strings.Contains(joined," login -s"){fmt.Println("... TicketExpiration 3600\n... User alice");return}
 if strings.Contains(joined," tickets"){fmt.Println("p4:1666 (alice) TESTTICKET");return}
 if strings.Contains(joined," describe "){
  if strings.Contains(joined," -S "){if e:=os.Getenv("P4_FAKE_SHELF_ERR");e!=""{fmt.Fprint(os.Stderr,e);os.Exit(1)};fmt.Print(os.Getenv("P4_FAKE_SHELF"));return}
  fmt.Print(os.Getenv("P4_FAKE_DESCRIBE"));return
 }
 panic("unexpected p4 command: "+joined)
}`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-p4"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v\n%s", err, output)
	}
	return path
}
