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
// along with Proton Mail Bridge. If not, see <https://www.gnu.org/licenses/>.

#ifndef BRIDGEPP_CLI_UTILS_H
#define BRIDGEPP_CLI_UTILS_H

namespace bridgepp {

QStringList stripStringParameterFromCommandLine(QString const &paramName, QStringList const &commandLineParams); ///< Remove a string parameter from a list of command-line parameters.
QStringList parseGoCLIStringArgument(QStringList const &args, QStringList const &paramNames); ///< Parse a command-line string argument as expected by go's CLI package.
QStringList cliArgsToStringList(int argc, char **argv); ///< Converts C-style command-line arguments to a string list.
QString mostRecentSessionID(QStringList const& args); ///< Returns the most recent sessionID parsed in command-line arguments.

}

#endif // BRIDGEPP_CLI_UTILS_H
