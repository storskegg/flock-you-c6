# flock-you-c6

Presently, this is testing NimBLE with the esp32-c6 in a productive manner.

The current flock-you firmware is giving...
- false negatives on two local flock safety cameras
- a false positive driving through a neighboring small town, for what appears to
  be a stripe credit card reader

As part of my experimenting with the c6, I'm implementing a BLE sniffer to
report all detected BLE devices. I'll test it on those two flock safety cameras
to see what their mac address prefixes are, UUID's, etc so that I can contribute
potential new hardware back to the community.

