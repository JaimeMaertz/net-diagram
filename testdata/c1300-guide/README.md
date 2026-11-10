# C1300 output from Cisco's CLI guide

Not a real switch. Each file is a command's example output from the *Cisco Catalyst 1300 Switches
Series CLI Guide* (Cisco's published PDF, last modified
2026-04-28). The column spacing is the PDF's layout, except in `show interfaces status` and
`show interfaces description`, whose spacing the PDF loses: there the words are the guide's and
the columns were lined up by hand. The LLDP detail is cut to the fields netdiagram reads. The examples
don't describe one switch, so the ports in one file don't match another's.

| File                            | Guide page |
| ------------------------------- | ---------- |
| show_system.txt                 | 1453       |
| show_inventory.txt              | 1438       |
| show_version.txt                | 515        |
| show_interfaces_status.txt      | 472        |
| show_interfaces_description.txt | 475        |
| show_lldp_neighbors.txt         | 892        |
| show_lldp_neighbors_gi1-0-1.txt | 892–893    |
| show_mac_address-table.txt      | 157        |
| show_power_inline.txt           | 1030–1031  |
| show_arp.txt                    | 605        |

Replace this folder's role with a real switch's output (`probe -vendor Cisco`, or a netdiagram read's
`raw\` folder) when one is read: put it in `testdata/c1300/` and point the tests at both.
