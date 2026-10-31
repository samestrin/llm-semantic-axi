import java.io.File

val greeting = "hi"

fun greet(name: String): String {
    return "$greeting $name"
}

class Helper {
    fun help() = greet("x")
}

object Config {
    val path = File(".")
}

println(greet("world"))
