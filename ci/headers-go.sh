#!/bin/bash

# Copyright 2017 DigitalOcean
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

read -r -d '' LICENSE <<EOF
/*
Copyright 2017 DigitalOcean

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
EOF

FILES=$(find . -name "*.go" -not -path "./vendor/*")
EXIT=0

for FILE in $FILES; do
        if head -n 1 "$FILE" | grep -q '// +build '; then
                # Remove +build comment and subsequent blank line.
                HEADERS=$(tail -n +3 "$FILE" | head -n 15)
        else
                HEADERS=$(head -n 15 "$FILE")
        fi
        if [ "$HEADERS" != "$LICENSE" ]; then
                echo "license headers not found in $FILE"
		EXIT=1
        fi
done

exit $EXIT
