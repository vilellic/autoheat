// Command sim runs the standard room scenarios and prints a summary, or a
// CSV trace of one scenario for plotting.
//
//	go run ./tools/sim
//	go run ./tools/sim -trace fireplace > fireplace.csv
//	go run ./tools/sim -params '{dwellMinutes: 20, lookaheadMinutes: 30}'
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vilellic/autoheat/internal/config"
	"github.com/vilellic/autoheat/internal/control"
	"github.com/vilellic/autoheat/internal/sim"
)

func main() {
	trace := flag.String("trace", "", "print a CSV trace of this scenario")
	seed := flag.Uint64("seed", 1, "random seed for sensor noise (first seed with -seeds)")
	seeds := flag.Int("seeds", 10, "average the summary over this many seeds")
	params := flag.String("params", "", "tuning overrides, as in a config room entry")
	flag.Parse()

	for _, s := range sim.Scenarios() {
		var err error
		if s.Params, err = config.ApplyTunables([]byte(*params), s.Params); err != nil {
			fmt.Fprintln(os.Stderr, "params:", err)
			os.Exit(1)
		}
		if *trace != "" && s.Name != *trace {
			continue
		}
		if *trace != "" {
			r := sim.Run(s, *seed)
			fmt.Println("minutes,room,sensor,target,mode,setTemp,fan,changed,reason")
			for _, p := range r.Points {
				c := p.Decision.Command
				fmt.Printf("%.1f,%.3f,%.1f,%.1f,%s,%d,%s,%v,%q\n", p.At.Minutes(), p.RoomTemp, p.Sensor, p.Target,
					c.Mode, c.SetTemp, c.Fan, p.Decision.Changed, p.Decision.Reason)
			}
			return
		}
		summarise(s, *seed, *seeds)
	}
	if *trace != "" {
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", *trace)
		os.Exit(1)
	}
}

// summarise prints mean statistics over several seeds: changes per hour for
// the whole run, comfort once settled (the second half), and the share of
// heating time at each fan speed.
func summarise(s sim.Scenario, first uint64, n int) {
	var changes, rms, worst float64
	var off, fanOnly, heat time.Duration
	var fans [control.High + 1]time.Duration
	for i := range n {
		r := sim.Run(s, first+uint64(i))
		changes += r.After(0).ChangesPerHour
		late := r.After(s.Duration / 2)
		rms += late.RMSError
		worst = max(worst, late.MaxAbsError)
		off += r.ModeTime(control.Off, 0)
		fanOnly += r.ModeTime(control.FanOnly, 0)
		heat += r.ModeTime(control.Heat, 0)
		for f := range fans {
			fans[f] += r.HeatFanTime(control.Fan(f), 0)
		}
	}
	k := float64(n)
	fmt.Printf("%-16s changes/h %.2f | settled: rms %.2f worst %.2f | off %5s fan_only %5s | heat fans %%",
		s.Name, changes/k, rms/k, worst,
		(off / time.Duration(n)).Round(time.Minute), (fanOnly / time.Duration(n)).Round(time.Minute))
	for f, d := range fans {
		share := 0.0
		if heat > 0 {
			share = 100 * float64(d) / float64(heat)
		}
		fmt.Printf(" %s %.0f", control.Fan(f), share)
	}
	fmt.Println()
}
