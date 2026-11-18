#!/usr/bin/env python3
"""Minimal SOCKS5 (CONNECT only, no auth) server for testing the bridge's socks5 path."""
import asyncio
import socket
import struct


async def pipe(reader, writer):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    except Exception:
        pass
    finally:
        try:
            writer.close()
        except Exception:
            pass


async def handle(reader, writer):
    try:
        ver, nmethods = await reader.readexactly(2)
        await reader.readexactly(nmethods)
        writer.write(b"\x05\x00")  # no auth
        await writer.drain()

        ver, cmd, rsv, atyp = await reader.readexactly(4)
        if atyp == 1:
            host = socket.inet_ntoa(await reader.readexactly(4))
        elif atyp == 3:
            ln = (await reader.readexactly(1))[0]
            host = (await reader.readexactly(ln)).decode()
        elif atyp == 4:
            host = socket.inet_ntop(socket.AF_INET6, await reader.readexactly(16))
        else:
            writer.close()
            return
        port = struct.unpack("!H", await reader.readexactly(2))[0]

        try:
            up_r, up_w = await asyncio.open_connection(host, port)
        except Exception:
            writer.write(b"\x05\x05\x00\x01\x00\x00\x00\x00\x00\x00")
            await writer.drain()
            writer.close()
            return

        writer.write(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
        await writer.drain()
        await asyncio.gather(pipe(reader, up_w), pipe(up_r, writer))
    except Exception:
        try:
            writer.close()
        except Exception:
            pass


async def main():
    server = await asyncio.start_server(handle, "127.0.0.1", 11080)
    print("socks5 on 127.0.0.1:11080", flush=True)
    async with server:
        await server.serve_forever()


asyncio.run(main())
