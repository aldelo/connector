package sdoperationstatus

/*
 * Copyright 2020-2021 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// go:generate gen-enumer -type SdOperationStatus

type SdOperationStatus int

const (
	UNKNOWN   SdOperationStatus = 0
	Submitted SdOperationStatus = 1
	Pending   SdOperationStatus = 2
	Success   SdOperationStatus = 3
	Fail      SdOperationStatus = 4
)

const (
	_SdOperationStatusKey_0 = "UNKNOWN"
	_SdOperationStatusKey_1 = "SUBMITTED"
	_SdOperationStatusKey_2 = "PENDING"
	_SdOperationStatusKey_3 = "SUCCESS"
	_SdOperationStatusKey_4 = "FAIL"
)

const (
	_SdOperationStatusCaption_0 = "UNKNOWN"
	_SdOperationStatusCaption_1 = "Submitted"
	_SdOperationStatusCaption_2 = "Pending"
	_SdOperationStatusCaption_3 = "Success"
	_SdOperationStatusCaption_4 = "Fail"
)

const (
	_SdOperationStatusDescription_0 = "UNKNOWN"
	_SdOperationStatusDescription_1 = "Submitted"
	_SdOperationStatusDescription_2 = "Pending"
	_SdOperationStatusDescription_3 = "Success"
	_SdOperationStatusDescription_4 = "Fail"
)