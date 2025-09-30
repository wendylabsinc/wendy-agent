import Hummingbird
import OTel

var config = OTel.Configuration.default
config.serviceName = "HelloHTTP"

let observability = try OTel.bootstrap(configuration: config)

// create router and add a single GET /hello route
let router = Router()

// Set up middleware for observability
router.add(middleware: TracingMiddleware())
router.add(middleware: LogRequestsMiddleware(.info))
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
