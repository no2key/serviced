package mocks

import metrics "github.com/control-center/serviced/metrics"
import mock "github.com/stretchr/testify/mock"
import time "time"

// MetricsClient is an autogenerated mock type for the MetricsClient type
type MetricsClient struct {
	mock.Mock
}

// GetAvailableStorage provides a mock function with given fields: _a0, _a1, _a2
func (_m *MetricsClient) GetAvailableStorage(_a0 time.Duration, _a1 string, _a2 ...string) (*metrics.StorageMetrics, error) {
	ret := _m.Called(_a0, _a1, _a2)

	var r0 *metrics.StorageMetrics
	if rf, ok := ret.Get(0).(func(time.Duration, string, ...string) *metrics.StorageMetrics); ok {
		r0 = rf(_a0, _a1, _a2...)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*metrics.StorageMetrics)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(time.Duration, string, ...string) error); ok {
		r1 = rf(_a0, _a1, _a2...)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetInstanceMemoryStats provides a mock function with given fields: _a0, _a1
func (_m *MetricsClient) GetInstanceMemoryStats(_a0 time.Time, _a1 ...metrics.ServiceInstance) ([]metrics.MemoryUsageStats, error) {
	ret := _m.Called(_a0, _a1)

	var r0 []metrics.MemoryUsageStats
	if rf, ok := ret.Get(0).(func(time.Time, ...metrics.ServiceInstance) []metrics.MemoryUsageStats); ok {
		r0 = rf(_a0, _a1...)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]metrics.MemoryUsageStats)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(time.Time, ...metrics.ServiceInstance) error); ok {
		r1 = rf(_a0, _a1...)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
