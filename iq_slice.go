/*
https://github.com/kylelemons/iq

Copyright 2010 Kyle Lemons
Copyright 2011 Google, Inc. (for changes on or after Feb. 22, 2011)

The accompanying software is licensed under the Common Development and
Distribution License, Version 1.0 (CDDL-1.0, the "License"); you may not use
any part of this software except in compliance with the License.

You may obtain a copy of the License at
	http://opensource.org/licenses/CDDL-1.0
More information about the CDDL can be found at
	http://hub.opensolaris.org/bin/view/Main/licensing_faq

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.  See the
License for the specific language governing permissions and limitations under
the License.
*/

package fetchbot

// sliceIQ creates an infinite buffered channel taking input on
// in and sending output to next.  SliceIQ should be run in its
// own goroutine.
func sliceIQ(in <-chan Command, next chan<- Command) {
	defer close(next)

	// pending events (this is the "infinite" part)
	pending := []Command{}

recv:
	for {
		// Ensure that pending always has values so the select can
		// multiplex between the receiver and sender properly
		if len(pending) == 0 {
			v, ok := <-in
			if !ok {
				// in is closed, flush values
				break
			}

			// We now have something to send
			pending = append(pending, v)
		}

		select {
		// Queue incoming values
		case v, ok := <-in:
			if !ok {
				// in is closed, flush values
				break recv
			}
			pending = append(pending, v)

		// Send queued values
		case next <- pending[0]:
			pending[0] = nil
			pending = pending[1:]
		}
	}

	// After in is closed, we may still have events to send
	for _, v := range pending {
		next <- v
	}
}
