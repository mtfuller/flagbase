package example

import (
	"fmt"
	"time"

	"github.com/mtfuller/flagbase/internal/color"
	"github.com/mtfuller/flagbase/internal/logger"
	"github.com/mtfuller/flagbase/internal/spinner"
)

// ProcessData demonstrates spinner and logging utilities.
func ProcessData(items []string, verbose bool) error {
	if verbose {
		logger.SetLevel(logger.DEBUG)
	}

	logger.Info("Starting data processing")
	logger.Debug("Processing %d items", len(items))

	s := spinner.New("Processing items...")
	s.Start()

	time.Sleep(2 * time.Second)

	s.Stop()

	color.Success("Successfully processed %d items", len(items))
	return nil
}

// Greet returns a simple greeting string.
func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

// Calculate performs an arithmetic operation.
func Calculate(a, b int, operation string) (int, error) {
	switch operation {
	case "add":
		return a + b, nil
	case "subtract":
		return a - b, nil
	case "multiply":
		return a * b, nil
	case "divide":
		if b == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return a / b, nil
	default:
		return 0, fmt.Errorf("unknown operation: %s", operation)
	}
}
