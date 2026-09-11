#!/usr/bin/env python3
"""Refresh only YuDesk conference TURN mappings; preserve other mappings."""
import argparse
import pathlib
import re
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--config', default=str(pathlib.Path.home() / 'bin/yudesk/server.conf'))
parser.add_argument('--upnpc', default='/opt/homebrew/bin/upnpc')
parser.add_argument('--igd', default='http://192.168.1.1:5431/gatedesc.xml')
args = parser.parse_args()

values = {}
for line in pathlib.Path(args.config).read_text().splitlines():
    line = line.strip()
    if not line or line.startswith('#'):
        continue
    key, sep, value = line.partition('=')
    if not sep or not re.fullmatch(r'[A-Z][A-Z0-9_]*', key):
        raise RuntimeError('Invalid server configuration line')
    values[key] = value.strip('"\'')

port = int(values['TURN_PORT'])
low, high = int(values['TURN_RELAY_MIN_PORT']), int(values['TURN_RELAY_MAX_PORT'])
assert 1024 <= port <= 65534 and 1024 <= low <= high <= 65534 and high - low < 256 and not low <= port <= high
route = subprocess.check_output(['/sbin/route', '-n', 'get', 'default'], text=True)
interface = re.search(r'interface:\s+(\S+)', route).group(1)
local_ip = subprocess.check_output(['/usr/sbin/ipconfig', 'getifaddr', interface], text=True).strip()
assert re.fullmatch(r'\d+\.\d+\.\d+\.\d+', local_ip)
base = [args.upnpc, '-u', args.igd]
listing = subprocess.check_output(base + ['-l'], text=True, timeout=20)
existing = {}
for line in listing.splitlines():
    if '->' not in line or not re.search(r'\b(?:TCP|UDP)\b', line):
        continue
    match = re.match(r'\s*\d+\s+(TCP|UDP)\s+(\d+)->([\d.]+):(\d+)\s+(.*)', line)
    if not match:
        raise RuntimeError('Unrecognized mapping entry; refusing to overwrite router state')
    protocol, external, address, internal, description = match.groups()
    existing[(protocol, int(external))] = (address, int(internal), description)

desired = [('TCP', port), ('UDP', port)] + [('UDP', value) for value in range(low, high + 1)]
for key in desired:
    if key in existing:
        address, internal, description = existing[key]
        if address != local_ip or internal != key[1] or 'YuDesk-conference' not in description:
            raise RuntimeError('Port is already owned by another mapping: %s %d' % key)
for index, (protocol, value) in enumerate(desired, 1):
    result = subprocess.run(base + ['-e', 'YuDesk-conference', '-a', local_ip, str(value), str(value), protocol], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
    if result.returncode != 0 or 'failed' in result.stdout.lower():
        raise RuntimeError('Cannot map %s %d: %s' % (protocol, value, result.stdout[-500:]))
    if index % 20 == 0:
        print('Verified %d YuDesk conference mappings' % index, flush=True)
print('YuDesk conference TURN: TCP/UDP %d, UDP %d-%d; other mappings preserved' % (port, low, high), flush=True)
