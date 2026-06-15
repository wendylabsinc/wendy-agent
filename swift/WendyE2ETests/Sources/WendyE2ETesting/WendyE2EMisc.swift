import Foundation

public let isAgentLinux = WendyE2EMachine.agent.os == .linux
public let isAgentWendyOS = WendyE2EMachine.agent.os == .wendyOS
public let isAgentMacOS = WendyE2EMachine.agent.os == .macOS
public let isAgentWindows = WendyE2EMachine.agent.os == .windows
public let isAgentLinuxOrWendyOS = isAgentLinux || isAgentWendyOS
