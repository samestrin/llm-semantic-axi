package com.example

import kotlin.math.PI

interface Shape {
    fun area(): Double
}

@Deprecated("use Circle2")
class Circle(private val r: Double) : Shape {
    constructor() : this(1.0)

    override fun area(): Double {
        return PI * r * r
    }

    companion object Factory {
        fun unit(): Circle = Circle(1.0)
    }

    class Inner {
        fun run() {}
    }
}

object Registry {
    fun register(s: Shape) {
        println(s)
    }
}

enum class Color {
    RED, GREEN;

    fun lower(): String = name.lowercase()
}

data class Point(val x: Int, val y: Int)

fun interface Action {
    fun invoke()
}

fun topLevel(x: Int): Int {
    return x * 2
}

fun String.shout(): String = uppercase()

sealed class Expr {
    object Zero : Expr()
    companion object {
        @JvmStatic
        fun zero(): Expr = Zero
    }
}
