// Copyright (c) 2024 Proton AG
//
// This file is part of Proton Mail Bridge.
//
// Proton Mail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Proton Mail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Proton Mail Bridge.  If not, see <https://www.gnu.org/licenses/>.

package configstatus_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ProtonMail/proton-bridge/v3/internal/configstatus"
	"github.com/stretchr/testify/require"
)

func TestConfigurationProgress_default(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dummy.json")
	config, err := configstatus.LoadConfigurationStatus(file)
	require.NoError(t, err)

	var builder = configstatus.ConfigProgressBuilder{}
	req := builder.New(config)

	require.Equal(t, "bridge.any.configuration", req.MeasurementGroup)
	require.Equal(t, "bridge_config_progress", req.Event)
	require.Equal(t, 0, req.Values.NbDay)
	require.Equal(t, 1, req.Values.NbDaySinceLast)
}

func TestConfigurationProgress_fed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dummy.json")
	var data = configstatus.ConfigurationStatusData{
		Metadata: configstatus.Metadata{Version: "1.0.0"},
		DataV1: configstatus.DataV1{
			PendingSince:   time.Now().AddDate(0, 0, -5),
			LastProgress:   time.Now().AddDate(0, 0, -2),
			Autoconf:       "Mr TBird",
			ClickedLink:    42,
			ReportSent:     false,
			ReportClick:    true,
			FailureDetails: "Not an error",
		},
	}
	require.NoError(t, dumpConfigStatusInFile(&data, file))

	config, err := configstatus.LoadConfigurationStatus(file)
	require.NoError(t, err)

	var builder = configstatus.ConfigProgressBuilder{}
	req := builder.New(config)

	require.Equal(t, "bridge.any.configuration", req.MeasurementGroup)
	require.Equal(t, "bridge_config_progress", req.Event)
	require.Equal(t, 5, req.Values.NbDay)
	require.Equal(t, 2, req.Values.NbDaySinceLast)
}

func TestConfigurationProgress_fed_year_change(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dummy.json")
	var data = configstatus.ConfigurationStatusData{
		Metadata: configstatus.Metadata{Version: "1.0.0"},
		DataV1: configstatus.DataV1{
			PendingSince:   time.Now().AddDate(-1, 0, -5),
			LastProgress:   time.Now().AddDate(0, 0, -2),
			Autoconf:       "Mr TBird",
			ClickedLink:    42,
			ReportSent:     false,
			ReportClick:    true,
			FailureDetails: "Not an error",
		},
	}
	require.NoError(t, dumpConfigStatusInFile(&data, file))

	config, err := configstatus.LoadConfigurationStatus(file)
	require.NoError(t, err)

	var builder = configstatus.ConfigProgressBuilder{}
	req := builder.New(config)

	require.Equal(t, "bridge.any.configuration", req.MeasurementGroup)
	require.Equal(t, "bridge_config_progress", req.Event)
	require.True(t, (req.Values.NbDay == 370) || (req.Values.NbDay == 371)) // leap year is accounted for in the simplest manner.
	require.Equal(t, 2, req.Values.NbDaySinceLast)
}
