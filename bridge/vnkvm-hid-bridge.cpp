// vnkvm-hid-bridge: root-only bridge between the virtual-nanokvm service and
// the Karabiner-DriverKit-VirtualHIDDevice daemon.
//
// The Karabiner daemon accepts commands only from root processes, so this small
// binary runs as root and forwards raw HID reports. All policy (trajectories,
// typing rhythm, access control) lives in the unprivileged Go service.
//
// Wire protocol (one line per command, one reply line per command):
//   K <modifiers> [<usage> ...]               keyboard report (up to 32 usages)
//   P <buttons> <dx> <dy> <vwheel> <hwheel>   pointing report (deltas -127..127)
//   R                                         release all keys and buttons
//   S                                         status
// Replies: "ok", "ready keyboard=<0|1> pointing=<0|1>", or "err <message>".

#include <pqrs/karabiner/driverkit/virtual_hid_device_driver.hpp>
#include <pqrs/karabiner/driverkit/virtual_hid_device_service.hpp>

#include <atomic>
#include <csignal>
#include <cstdio>
#include <cstring>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

namespace hid_report = pqrs::karabiner::driverkit::virtual_hid_device_driver::hid_report;
namespace service = pqrs::karabiner::driverkit::virtual_hid_device_service;

namespace {

std::atomic<bool> keyboard_ready(false);
std::atomic<bool> pointing_ready(false);
std::mutex client_mutex;
std::unique_ptr<service::client> client;

std::string handle(const std::string& line) {
  std::istringstream in(line);
  std::string op;
  in >> op;

  std::lock_guard<std::mutex> lock(client_mutex);

  if (op == "S") {
    return std::string("ready keyboard=") + (keyboard_ready ? "1" : "0") +
           " pointing=" + (pointing_ready ? "1" : "0");
  }

  if (op == "K") {
    if (!keyboard_ready) {
      return "err keyboard not ready";
    }
    unsigned int modifiers = 0;
    if (!(in >> modifiers) || modifiers > 0xff) {
      return "err bad modifiers";
    }
    hid_report::keyboard_input report;
    for (int bit = 0; bit < 8; ++bit) {
      if (modifiers & (1u << bit)) {
        report.modifiers.insert(static_cast<hid_report::modifier>(1u << bit));
      }
    }
    unsigned int usage = 0;
    while (in >> usage) {
      if (usage > 0xffff) {
        return "err bad usage";
      }
      report.keys.insert(static_cast<uint16_t>(usage));
    }
    client->async_post_report(report);
    return "ok";
  }

  if (op == "P") {
    if (!pointing_ready) {
      return "err pointing not ready";
    }
    unsigned long buttons = 0;
    int dx = 0, dy = 0, vwheel = 0, hwheel = 0;
    if (!(in >> buttons >> dx >> dy >> vwheel >> hwheel)) {
      return "err bad pointing report";
    }
    for (int v : {dx, dy, vwheel, hwheel}) {
      if (v < -127 || v > 127) {
        return "err delta out of range";
      }
    }
    hid_report::pointing_input report;
    for (int b = 1; b <= 32; ++b) {
      if (buttons & (1ul << (b - 1))) {
        report.buttons.insert(static_cast<uint8_t>(b));
      }
    }
    // The report fields are uint8_t carrying two's-complement signed deltas.
    report.x = static_cast<uint8_t>(static_cast<int8_t>(dx));
    report.y = static_cast<uint8_t>(static_cast<int8_t>(dy));
    report.vertical_wheel = static_cast<uint8_t>(static_cast<int8_t>(vwheel));
    report.horizontal_wheel = static_cast<uint8_t>(static_cast<int8_t>(hwheel));
    client->async_post_report(report);
    return "ok";
  }

  if (op == "R") {
    if (keyboard_ready) {
      client->async_post_report(hid_report::keyboard_input());
    }
    if (pointing_ready) {
      client->async_post_report(hid_report::pointing_input());
    }
    return "ok";
  }

  return "err unknown command";
}

void release_all() {
  handle("R");
}

void serve(int conn) {
  std::string buffer;
  char chunk[4096];
  for (;;) {
    ssize_t n = read(conn, chunk, sizeof(chunk));
    if (n <= 0) {
      return;
    }
    buffer.append(chunk, static_cast<size_t>(n));
    size_t pos;
    while ((pos = buffer.find('\n')) != std::string::npos) {
      std::string reply = handle(buffer.substr(0, pos)) + "\n";
      buffer.erase(0, pos + 1);
      if (write(conn, reply.data(), reply.size()) < 0) {
        return;
      }
    }
  }
}

int usage() {
  std::cerr << "usage: vnkvm-hid-bridge --socket <path> --owner <uid>" << std::endl;
  return 2;
}

} // namespace

int main(int argc, char** argv) {
  std::string socket_path;
  long owner = -1;
  for (int i = 1; i + 1 < argc; i += 2) {
    if (std::strcmp(argv[i], "--socket") == 0) {
      socket_path = argv[i + 1];
    } else if (std::strcmp(argv[i], "--owner") == 0) {
      owner = std::strtol(argv[i + 1], nullptr, 10);
    } else {
      return usage();
    }
  }
  if (socket_path.empty() || owner < 0) {
    return usage();
  }
  if (geteuid() != 0) {
    std::cerr << "vnkvm-hid-bridge must run as root (Karabiner daemon rule)" << std::endl;
    return 1;
  }

  std::signal(SIGPIPE, SIG_IGN);

  pqrs::dispatcher::extra::initialize_shared_dispatcher();
  client = std::make_unique<service::client>();
  client->warning_reported.connect([](auto&& message) {
    std::cerr << "warning: " << message << std::endl;
  });
  client->connected.connect([] {
    std::cerr << "connected to Karabiner daemon" << std::endl;
    service::virtual_hid_keyboard_parameters parameters;
    parameters.set_country_code(pqrs::hid::country_code::us);
    client->async_virtual_hid_keyboard_initialize(parameters);
    client->async_virtual_hid_pointing_initialize();
  });
  client->connect_failed.connect([](auto&& error_code) {
    std::cerr << "connect_failed: " << error_code.message() << std::endl;
  });
  client->closed.connect([] {
    keyboard_ready = false;
    pointing_ready = false;
    std::cerr << "daemon connection closed" << std::endl;
  });
  client->driver_version_mismatched.connect([](auto&& mismatched) {
    if (mismatched) {
      std::cerr << "driver version mismatched: update the Karabiner driver" << std::endl;
    }
  });
  client->virtual_hid_keyboard_ready.connect([](auto&& ready) { keyboard_ready = ready; });
  client->virtual_hid_pointing_ready.connect([](auto&& ready) { pointing_ready = ready; });
  client->async_start();

  // The socket is owned by the service user and mode 0600, so only that user
  // (and root) can drive the virtual devices.
  int listener = socket(AF_UNIX, SOCK_STREAM, 0);
  sockaddr_un addr{};
  addr.sun_family = AF_UNIX;
  if (socket_path.size() >= sizeof(addr.sun_path)) {
    std::cerr << "socket path too long" << std::endl;
    return 1;
  }
  std::strncpy(addr.sun_path, socket_path.c_str(), sizeof(addr.sun_path) - 1);
  unlink(socket_path.c_str());
  mode_t old_umask = umask(0077);
  if (bind(listener, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) != 0) {
    std::perror("bind");
    return 1;
  }
  umask(old_umask);
  if (chown(socket_path.c_str(), static_cast<uid_t>(owner), static_cast<gid_t>(-1)) != 0 ||
      chmod(socket_path.c_str(), 0600) != 0) {
    std::perror("chown/chmod");
    return 1;
  }
  if (listen(listener, 1) != 0) {
    std::perror("listen");
    return 1;
  }
  std::cerr << "listening on " << socket_path << std::endl;

  // One client at a time: the virtual-nanokvm service. Whatever happens to it,
  // release every key and button so nothing stays stuck down.
  for (;;) {
    int conn = accept(listener, nullptr, nullptr);
    if (conn < 0) {
      continue;
    }
    serve(conn);
    close(conn);
    release_all();
  }
}
