package main

import (
	"math"
	"math/rand"
	"testing"
)

// Calibration should recover a zone's world->map transform from monsters seen
// near their spawn points, starting from the default guess.
func TestCalibrateFromSpawns(t *testing.T) {
	loadZones()
	for _, zone := range []string{"verteron", "altgard", "chaotic-lower-reshanta"} {
		zi := zones[zone]
		if zi == nil {
			t.Fatalf("zone %s missing", zone)
		}
		truth := Calib{S: 0.020353 * zi.Frame / 16384, OX: zi.Frame/2 + 37, OY: zi.Frame/2 - 21}
		if zone != "verteron" { // far from the default guess
			truth = Calib{S: 0.020353, OX: zi.Frame*0.3 + 911, OY: zi.Frame*0.7 - 433}
		}
		r := rand.New(rand.NewSource(1))
		var pts []obs
		// a cluster of monsters around one spawn area, like a farming session
		var cx, cy float64
		for name, sp := range zi.Spawns {
			if len(sp) >= 8 {
				cx, cy = sp[0][0], sp[0][1]
				_ = name
				break
			}
		}
		for name, sp := range zi.Spawns {
			for _, q := range sp {
				if math.Hypot(q[0]-cx, q[1]-cy) > 300*zi.Frame/16384 {
					continue
				}
				wx := (q[0] - truth.OX + r.NormFloat64()*2) / truth.S
				wy := (q[1] - truth.OY + r.NormFloat64()*2) / truth.S
				pts = append(pts, obs{zone, name, wx, wy})
			}
		}
		if len(pts) < 15 {
			t.Logf("%s: only %d points near cluster, skipping", zone, len(pts))
			continue
		}
		st := &State{Calibs: map[string]Calib{}, obs: pts}
		delete(builtinCalib, zone)
		st.maybeCalibrate(zone)
		c, ok := st.Calibs[zone]
		if !ok {
			t.Fatalf("%s: not calibrated (%d pts)", zone, len(pts))
		}
		// what matters: monsters land where they should around the farming spot
		var d float64
		for _, p := range pts {
			d += math.Hypot(c.S*p.x+c.OX-(truth.S*p.x+truth.OX), c.S*p.y+c.OY-(truth.S*p.y+truth.OY))
		}
		if d /= float64(len(pts)); d > 3 {
			t.Fatalf("%s: off by %.1f px on average: got %+v want %+v", zone, d, c, truth)
		}
		t.Logf("%s: %d pts, err %.2f px", zone, len(pts), c.Err)
	}
}
