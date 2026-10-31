using System;

namespace Shop.Orders
{
    public interface IRepository
    {
        Order Find(int id);
    }

    [Serializable]
    public class OrderService : IRepository
    {
        private readonly int _limit;

        public OrderService(int limit)
        {
            _limit = limit;
        }

        ~OrderService() { }

        public Order Find(int id)
        {
            return new Order(id, 0m);
        }

        public int Limit => _limit;

        private class Cache
        {
            void Clear()
            {
                return;
            }
        }
    }

    public struct Money
    {
        public decimal Amount;

        public Money Add(Money other) => new Money { Amount = Amount + other.Amount };
    }

    public record Order(int Id, decimal Total)
    {
        public bool IsFree() => Total == 0m;
    }

    public enum Status { Open, Closed }
}

namespace Shop.Billing;

public record struct Invoice(int Number);

public static class Extensions
{
    public static string Shout(this string s)
    {
        return s.ToUpper();
    }
}
