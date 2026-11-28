-- Placing or resolving an order changes what an automating app may size
-- against, so it bumps the account revision like cash and position changes.
CREATE TRIGGER revision_order_insert AFTER INSERT ON orders BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER revision_order_status AFTER UPDATE OF status,filled_qty ON orders
WHEN OLD.status IS NOT NEW.status OR OLD.filled_qty IS NOT NEW.filled_qty BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
