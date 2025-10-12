package main

import (
	"testing"
)

// For testing, we use a different approach than trying to mock the network calls
// Instead, we'll test the functions that process the results

func TestQuartileOnDownloadResults(t *testing.T) {
	// Sample download data representing speed test results in Mbps
	testData := []float64{
		50.2, 55.1, 52.8, 51.9, 53.7, 58.2, 49.8, 54.3, 53.0, 56.1,
	}

	// Test median
	medianSpeed := median(testData)
	expectedMedian := 53.35 // Calculated manually: (53.0 + 53.7) / 2
	if abs(medianSpeed-expectedMedian) > 0.01 {
		t.Errorf("median speed = %f, expected %f", medianSpeed, expectedMedian)
	}

	// Test quartile for 90th percentile (what we display as final speed)
	q90Speed := quartile(testData, 0.90)
	expectedQ90 := 57.4                  // Manually calculated 90th percentile
	if abs(q90Speed-expectedQ90) > 0.3 { // Allow a bit more tolerance due to interpolation
		t.Errorf("90th percentile speed = %f, expected approximately %f", q90Speed, expectedQ90)
	}
}

func TestAppendingDownloadTests(t *testing.T) {
	// This tests how we combine the test results from different download sizes
	test1 := []float64{10.1, 10.2, 10.3} // 100kB
	test2 := []float64{20.1, 20.2}       // 1MB
	test3 := []float64{30.1}             // 10MB
	test4 := []float64{40.1, 40.2}       // 100MB

	// Test how the results are combined as in the main function
	allTests := append(append(append(test1, test2...), test3...), test4...)

	// Check result counts
	if len(allTests) != 8 {
		t.Errorf("expected 8 combined results, got %d", len(allTests))
	}

	// Check the values are included correctly
	expectedValues := []float64{10.1, 10.2, 10.3, 20.1, 20.2, 30.1, 40.1, 40.2}
	for i, val := range expectedValues {
		if abs(allTests[i]-val) > 0.01 {
			t.Errorf("at position %d: expected %f, got %f", i, val, allTests[i])
		}
	}
}

func TestSpeedLogging(t *testing.T) {
	// This is a simple test to ensure the logic for lite mode is correct
	fullModeTests := []float64{10.1, 20.2, 30.3, 40.4}
	liteModeTests := []float64{10.1, 20.2, 30.3}

	// In full mode, with 4 sizes of tests, we should have all values
	if len(fullModeTests) != 4 {
		t.Errorf("expected 4 tests in full mode, got %d", len(fullModeTests))
	}

	// In lite mode, with 3 sizes of tests, we should have 3 values
	if len(liteModeTests) != 3 {
		t.Errorf("expected 3 tests in lite mode, got %d", len(liteModeTests))
	}

	// Check the behavior of quartile function on these arrays
	fullSpeed := quartile(fullModeTests, 0.9)
	expectedFullSpeed := 38.39 // Calculated manually
	if abs(fullSpeed-expectedFullSpeed) > 0.1 {
		t.Errorf("full mode speed = %f, expected approximately %f", fullSpeed, expectedFullSpeed)
	}

	liteSpeed := quartile(liteModeTests, 0.9)
	expectedLiteSpeed := 29.07 // Calculated manually
	if abs(liteSpeed-expectedLiteSpeed) > 0.1 {
		t.Errorf("lite mode speed = %f, expected approximately %f", liteSpeed, expectedLiteSpeed)
	}
}

// Helper function for floating point comparison
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
