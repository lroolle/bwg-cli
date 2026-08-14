package kiwivm_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lroolle/bwg-cli/kiwivm"
)

// The common case: read the fleet-relevant numbers off one VPS
// without any possibility of changing it.
func Example_readOnly() {
	c := kiwivm.New(os.Getenv("BWG_VEID"), os.Getenv("BWG_API_KEY"),
		kiwivm.ReadOnly())

	// A mutation is refused before any HTTP request happens.
	if err := c.Restart(context.Background()); kiwivm.IsReadOnly(err) {
		fmt.Println("restart refused: client is read-only")
	}
	// Output: restart refused: client is read-only
}

// Bandwidth applies the location multiplier to both the allowance and
// the counter, which is what the KiwiVM panel shows. Percent is the
// figure to trust: the multiplier scales both sides equally.
func ExampleServiceInfo_Bandwidth() {
	const gib = 1024 * 1024 * 1024
	info := &kiwivm.ServiceInfo{
		PlanMonthlyData:       kiwivm.Int(1000 * gib),
		DataCounter:           kiwivm.Int(250 * gib),
		MonthlyDataMultiplier: 3,
	}

	b := info.Bandwidth()
	fmt.Printf("%d GiB of %d GiB (%.0f%%), multiplier %dx\n",
		b.Used/gib, b.Total/gib, b.Percent, b.Multiplier)
	// Output: 750 GiB of 3000 GiB (25%), multiplier 3x
}

// Errors are classified so callers can branch without matching text.
func Example_errorHandling() {
	c := kiwivm.New("1347645", "private_key", kiwivm.WithTimeout(10*time.Second))

	info, err := c.ServiceInfo(context.Background())
	switch {
	case kiwivm.IsAuth(err):
		log.Fatal("the veid/api_key pair does not work")
	case kiwivm.IsLocked(err):
		log.Print("the VPS is busy with another task; the error carries progress")
	case kiwivm.IsTransient(err):
		log.Print("temporary — retry unchanged")
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Println(info.Hostname)
	}
}

// Ops is the registry every surface reads. Building a menu of what a
// client may do means asking it, not guessing.
func ExampleClient_Can() {
	c := kiwivm.New("1347645", "private_key", kiwivm.ReadOnly())

	for _, endpoint := range []string{"getServiceInfo", "snapshot/create", "reinstallOS"} {
		op, _ := kiwivm.LookupOp(endpoint)
		allowed, _ := c.Can(endpoint)
		fmt.Printf("%-16s %-12s allowed=%v\n", endpoint, op.Risk, allowed)
	}
	// Output:
	// getServiceInfo   read         allowed=true
	// snapshot/create  write        allowed=false
	// reinstallOS      destructive  allowed=false
}
