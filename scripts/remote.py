"""Explicit, bounded RemotePairing operations. Never fall back to local transport."""
import argparse
import asyncio
from contextlib import AsyncExitStack
import json
import os
from pathlib import Path
import urllib.request


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=["pair", "doctor", "screenshot", "run"])
    p.add_argument("output", nargs="?")
    p.add_argument("--udid", required=True, help="Hardware UDID; not CoreDevice UUID")
    p.add_argument("--host", help="Explicit Tailscale DNS name or IP; no LAN discovery fallback")
    p.add_argument("--pair-port", type=int, default=49152, help="Last discovered RemotePairing port; may change")
    p.add_argument("--seconds", type=int, default=60, help="Runner lifetime, 1..600 seconds")
    p.add_argument("--runner", help="run only: runner bundle id, <your prefix>.com.mobilenext.devicekit-iosUITests.xctrunner")
    return p


def validate(args):
    if args.action != "pair" and not args.host:
        raise ValueError("--host is required; remote operations never fall back to LAN")
    if not 1 <= args.pair_port <= 65535 or not 1 <= args.seconds <= 600:
        raise ValueError("Port must be 1..65535 and seconds 1..600")
    if args.action == "run" and not args.runner:
        raise ValueError("run needs --runner <your prefix>.com.mobilenext.devicekit-iosUITests.xctrunner")
    if (args.action == "screenshot") != bool(args.output):
        raise ValueError("Only screenshot takes an output path, and requires it")
    if args.action == "screenshot" and Path(args.output).exists():
        raise ValueError("Screenshot output already exists; choose a new path")


def describe_error(error):
    # NSError trees contain the real signing/trust failure under NSUnderlyingError.
    item = getattr(error, "error", None)
    lines = []
    while item is not None:
        info = getattr(item, "user_info", None) or {}
        lines.append(f"{item.domain} ({item.code}): " + str(info.get("NSLocalizedDescription", "")))
        item = info.get("NSUnderlyingError")
    return "; ".join(lines) or f"{type(error).__name__}: {error}"


def agent_probe(host, port):
    authority = f"[{host}]" if ":" in host else host
    request = urllib.request.Request(f"http://{authority}:{port}/rpc",
        data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "device.info"}).encode(),
        headers={"Content-Type": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=3) as response:
        result = json.load(response)
    return "result" in result and not result.get("error")


async def pair(args):
    from pymobiledevice3.lockdown import create_using_usbmux
    from pymobiledevice3.remote.tunnel_service import RemotePairingLockdownService, get_remote_pairing_tunnel_services
    from pymobiledevice3.exceptions import RemotePairingCompletedError
    # Existing host trust only. Never replace/reset a user's pairing record.
    client = await create_using_usbmux(serial=args.udid, autopair=False)
    try:
        if not client.paired:
            raise RuntimeError("No existing Mac trust; pair locally with Apple tools first")
        service = await RemotePairingLockdownService.create(client)
        try:
            try:
                await service.connect(autopair=True)
            except RemotePairingCompletedError:
                await service.close()
                service = await RemotePairingLockdownService.create(client)
                await service.connect(autopair=False)
            if service.encryption_key is None:
                raise RuntimeError("RemotePairing verification failed")
        finally:
            await service.close()
        endpoints = await get_remote_pairing_tunnel_services(udid=args.udid, bonjour_timeout=3)
        ports = sorted({endpoint.port for endpoint in endpoints})
        for endpoint in endpoints:
            await endpoint.close()
        print(json.dumps({"paired": True, "udid": args.udid, "pair_ports": ports}))
    finally:
        await client.close()


async def remote(args):
    from pymobiledevice3.remote import tunnel_service
    from pymobiledevice3.remote.userspace_tunnel import UserspaceDialPlane
    from pymobiledevice3.remote.remote_service_discovery import RemoteServiceDiscoveryService
    from pymobiledevice3.services.dvt.instruments.screenshot import Screenshot
    from pymobiledevice3.cli.developer.dvt import DvtProvider
    report = {"host": args.host, "udid": args.udid, "paired": False,
              "developer_tunnel": False, "screenshot": False, "agent": False}
    tunnel_service.USE_USERSPACE_TUNNEL = True
    try:
        async with AsyncExitStack() as stack:
            provider = await tunnel_service.create_core_device_tunnel_service_using_remotepairing(
                args.udid, args.host, args.pair_port, autopair=False)
            stack.push_async_callback(provider.close)
            if provider.encryption_key is None:
                raise RuntimeError("RemotePairing authentication failed; pair locally first")
            report["paired"] = True
            tunnel = await stack.enter_async_context(provider.start_tcp_tunnel())
            tun = tunnel.client.tun
            tun.set_peer(tunnel.address)
            dial = await stack.enter_async_context(UserspaceDialPlane(tun, tunnel.address))
            rsd = RemoteServiceDiscoveryService((tunnel.address, tunnel.port),
                open_connection=dial.dial, auxiliary_metadata=tunnel.auxiliary_metadata)
            stack.push_async_callback(rsd.close)
            await rsd.connect()
            if rsd.udid != args.udid:
                raise RuntimeError("Connected device identity differs from requested UDID")
            report["developer_tunnel"] = True
            if args.action in ("doctor", "screenshot"):
                async with Screenshot(DvtProvider(rsd)) as shot:
                    image = await shot.get_screenshot()
                    if not image:
                        raise RuntimeError("Empty screenshot")
                    report["screenshot"] = True
                    if args.action == "screenshot":
                        # Exclusive creation: don't overwrite user files, including races.
                        with open(args.output, "xb") as output:
                            output.write(image)
                        report["output"] = str(Path(args.output).resolve())
            try:
                report["agent"] = await asyncio.to_thread(agent_probe, args.host, int(os.environ["IPH_PORT"]))
            except (OSError, ValueError):
                pass
            if args.action == "run":
                from pymobiledevice3.services.dvt.testmanaged.xcuitest import TestConfig, XCUITestService
                cfg = await TestConfig.create_for(rsd, args.runner, None)
                cfg.runner_app_env = {"DEVICEKIT_LISTEN_HOST": "0.0.0.0", "DEVICEKIT_LISTEN_PORT": os.environ["IPH_PORT"]}
                # Foreground by design: deadline and cleanup are owned by this process.
                print(json.dumps(report), flush=True)
                await XCUITestService(rsd).run(cfg, timeout=args.seconds)
                report["runner_finished"] = True
    except BaseException as error:
        report["error"] = describe_error(error)
        print(json.dumps(report), flush=True)
        raise
    finally:
        tunnel_service.USE_USERSPACE_TUNNEL = False
    print(json.dumps(report), flush=True)
    # doctor means control-ready, not merely a screenshot-capable tunnel.
    return args.action != "doctor" or report["agent"]


def main():
    args = parser().parse_args()
    try:
        validate(args)
        operation = pair(args) if args.action == "pair" else remote(args)
        result = asyncio.run(asyncio.wait_for(operation, args.seconds + 60 if args.action == "run" else 60))
        return 1 if result is False else 0
    except Exception as error:
        print(describe_error(error), file=__import__("sys").stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
