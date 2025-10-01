import Hummingbird
import OTel

var config = OTel.Configuration.default
config.serviceName = "HelloHTTP"

config.logs.otlpExporter.endpoint = "http://192.168.7.2:4317"
config.logs.otlpExporter.protocol = .grpc
config.logs.exporter = .otlp
config.logs.batchLogRecordProcessor.scheduleDelay = .seconds(1)

config.metrics.otlpExporter.endpoint = "http://192.168.7.2:4317"
config.metrics.otlpExporter.protocol = .grpc
config.metrics.exporter = .otlp
config.metrics.exportInterval = .seconds(1)

config.traces.otlpExporter.endpoint = "http://192.168.7.2:4317"
config.traces.otlpExporter.protocol = .grpc
config.traces.exporter = .otlp
config.traces.batchSpanProcessor.scheduleDelay = .seconds(1)

let observability = try OTel.bootstrap(configuration: config)

// create router and add a single GET /hello route
let router = Router()

// Set up middleware for observability
router.add(middleware: TracingMiddleware())
router.add(middleware: LogRequestsMiddleware(.error))
router.add(middleware: MetricsMiddleware())

router.get("hello") { request, _ -> String in
    return "Hello"
}
// create application using router
var app = Application(
    router: router,
    configuration: .init(address: .hostname("0.0.0.0", port: 8080))
)
app.addServices(observability)

// run hummingbird application
try await app.runService()
