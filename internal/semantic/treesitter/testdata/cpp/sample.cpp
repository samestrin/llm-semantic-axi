#include <string>

namespace geo {

template <typename T>
class Box {
public:
    Box(T v) : value_(v) {}
    ~Box();
    T get() const { return value_; }
private:
    T value_;
};

struct Point {
    int x;
    int y;
};

enum class Color { Red, Green };

}  // namespace geo

int geo_helper(int a) {
    return a + 1;
}

int Widget::size() const {
    return 3;
}

static int *make(int n) { return nullptr; }

extern "C" {
void c_entry(void) {}
}
