#ifndef LEGACY_H
#define LEGACY_H

struct node {
    int value;
    struct node *next;
};

static inline int node_sum(struct node *n) {
    int total = 0;
    while (n) {
        total += n->value;
        n = n->next;
    }
    return total;
}

int node_count(struct node *n) {
    return n ? 1 + node_count(n->next) : 0;
}

#endif
