// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "MoodiesMenuBar",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "MoodiesMenuBar", targets: ["MoodiesMenuBar"]),
    ],
    targets: [
        .executableTarget(
            name: "MoodiesMenuBar",
            path: "Sources/MoodiesMenuBar"
        ),
    ]
)
