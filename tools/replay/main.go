// Command replay feeds the inputs recorded in a v1 autoheat.log through the
// new controller and compares its decisions with v1's.
//
//	go run ./tools/replay path/to/v1/autoheat.log
//	go run ./tools/replay -v path/to/v1/autoheat.log   # every call
//
// This is open loop: each call sees the pump as v1 left it, so it is a
// sanity check (does v2 broadly agree, and never break policy), not a
// prediction of how v2 would have run the room.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/vilellic/autoheat/internal/control"
)

var (
	inputLine  = regexp.MustCompile(`^(\S+ \S+) InputParameters = \{(\S+) (\S+) \S+ \{.*?\} (\S+) (\d+) (\d+) (\d+) (\d+) (\w+) (\w+) (\d+)\}`)
	resultLine = regexp.MustCompile(`^\S+ \S+ Result state = \{(\S+) (\d+) (\d+) `)
)

// v1Fan maps v1 fan speeds (Auto=0, Quiet=1, ..., High=5). Auto becomes low.
func v1Fan(s string) control.Fan {
	n := atoi(s)
	if n == 0 {
		return control.Low
	}
	return control.Fan(n - 1)
}

func atoi(s string) int     { n, _ := strconv.Atoi(s); return n }
func atof(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

type call struct {
	at    time.Time
	in    control.Input
	v1    control.PumpState
	hasV1 bool
}

func main() {
	verbose := flag.Bool("v", false, "print every call")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay [-v] autoheat.log")
		os.Exit(2)
	}
	calls, err := read(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var room control.Room
	p := control.DefaultParams()
	names := [3]string{"down", "hold", "up"}
	var matrix [3][3]int // [v1][v2]
	var v1Changes, v2Changes int
	for _, c := range calls {
		d := room.Decide(c.in, p, c.at)
		if !c.hasV1 {
			continue
		}
		lad := control.BuildLadder(c.in.Policy, p)
		from, _ := lad.Fix(c.in.Observed)
		dir := func(to control.PumpState) int {
			if to.Same(c.in.Observed) {
				return 1
			}
			a, b := lad.Locate(from), lad.Locate(to)
			switch {
			case b > a:
				return 2
			case b < a:
				return 0
			}
			return 1
		}
		d1, d2 := dir(c.v1), dir(d.Command)
		matrix[d1][d2]++
		if d1 != 1 {
			v1Changes++
		}
		if d.Changed {
			v2Changes++
		}
		if *verbose {
			fmt.Printf("%s room %.2f target %.2f  pump %-18s v1 %-18s v2 %-18s %s\n",
				c.at.Format("01-02 15:04"), c.in.RoomTemp, c.in.Target, c.in.Observed, c.v1, d.Command, d.Reason)
		}
	}

	fmt.Printf("%d calls from %s to %s\n", len(calls), calls[0].at.Format(time.DateTime), calls[len(calls)-1].at.Format(time.DateTime))
	fmt.Printf("changes: v1 %d, v2 %d\n\n", v1Changes, v2Changes)
	fmt.Println("rows v1, columns v2:")
	fmt.Printf("%8s %6s %6s %6s\n", "", names[0], names[1], names[2])
	for i, row := range matrix {
		fmt.Printf("%8s %6d %6d %6d\n", names[i], row[0], row[1], row[2])
	}
}

func read(path string) ([]call, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var calls []call
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := inputLine.FindStringSubmatch(line); m != nil {
			at, err := time.Parse("2006/01/02 15:04:05", m[1])
			if err != nil {
				return nil, err
			}
			mode, _ := control.ParseMode(m[4])
			calls = append(calls, call{at: at, in: control.Input{
				Target:   atof(m[2]),
				RoomTemp: atof(m[3]),
				Observed: control.PumpState{Mode: mode, SetTemp: atoi(m[5]), Fan: v1Fan(m[6])},
				Policy: control.Policy{
					MinSetTemp:   atoi(m[7]),
					MaxSetTemp:   atoi(m[8]),
					CanUseFan:    m[9] == "true",
					CanSwitchOff: m[10] == "true",
					MaxFan:       v1Fan(m[11]),
				},
			}})
			continue
		}
		if m := resultLine.FindStringSubmatch(line); m != nil && len(calls) > 0 {
			mode, _ := control.ParseMode(m[1])
			last := &calls[len(calls)-1]
			last.v1, last.hasV1 = control.PumpState{Mode: mode, SetTemp: atoi(m[2]), Fan: v1Fan(m[3])}, true
		}
	}
	if len(calls) == 0 {
		return nil, fmt.Errorf("%s: no v1 input lines found", path)
	}
	return calls, sc.Err()
}
